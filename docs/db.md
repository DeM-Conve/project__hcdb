# DB — the orchestration layer

Package: `db/` — files: `db_types.go`, `db.go`, `db_operations.go`

## Why it exists

`db` is the public API and the only place that knows about all the other packages. Everything
below it — WAL, memtable, SSTable, bloom filter, compaction — is a component that doesn't know
the others exist. `db` wires them into an LSM tree and owns the two rules that make it correct:

1. **Write order**: WAL before memtable.
2. **Read order**: memtable, then SSTables newest → oldest, stopping at the first answer.

## Type

```go
type DB struct {
    mu       sync.RWMutex
    wal      *wal.WAL
    memtable *memtable.MemTable
    sstables []*sstable.SSTable   // INVARIANT: newest first
    conf     config.Config
}
```

`sstables` being ordered newest-first is the single most important invariant in the codebase.
Every read depends on it — it's what makes a newer value shadow an older one, and a tombstone
shadow a live value.

## `Open(conf)`

```go
os.MkdirAll(conf.SSTDir, 0755)
walObj, _ := wal.Open(conf.WALPath)
mem, _   := rebuildMemtable(walObj)      // replay the log
tables,_ := sstable.OpenAllInDir(conf.SSTDir)
return &DB{wal, mem, tables, conf}
```

`rebuildMemtable` calls `walObj.Replay()` and applies each entry by type:

```go
case config.OP_DELETE: mem.Delete(e.Key)
case config.OP_PUT:    mem.Put(e.Key, e.Value)
```

Replay is in write order, so later writes overwrite earlier ones and the memtable ends up in
exactly the state it had before the crash — minus whatever was lost to the WAL's 10-write sync
window, and minus any torn tail record that replay truncated.

`OpenAllInDir` sorts filenames descending (they're `UnixNano` timestamps), which establishes the
newest-first invariant at startup.

## `Put(key, value)` (`db_operations.go`)

```go
if err := db.wal.Put(key, value); err != nil { return err }   // durability FIRST
db.memtable.Put([]byte(key), []byte(value))

if db.memtable.Size() >= config.DEFAULT_MEMTABLE_FLUSH_SIZE {
    return db.flushMemtable()
}
```

The WAL write comes first and its error short-circuits, so the memtable can never contain a
write that isn't in the log. If it were the other way round, a crash between the two steps would
lose an acknowledged write.

`Put` takes **no lock** — it relies on the memtable's and (implicitly) the WAL's internal
synchronisation. `flushMemtable` takes `db.mu.Lock()` itself.

## `Delete(key)`

```go
if err := db.wal.Delete(key); err != nil { return err }
db.memtable.Delete([]byte(key))
```

Same ordering. Writes a tombstone in both places. **No flush-size check** — see limitations.

## `Get(key)`

```go
db.mu.RLock(); defer db.mu.RUnlock()

if val, ok := db.memtable.Get([]byte(key)); ok {
    return val, true              // memtable is always the freshest
}
return db.searchSSTables([]byte(key))
```

### `searchSSTables`

```go
for _, sst := range db.sstables {          // newest → oldest
    val, st, err := sst.Lookup(key)
    if err != nil                  { return nil, false }
    if st == sstable.KEY_DELETED   { return nil, false }   // STOP — deleted
    if st == sstable.KEY_FOUND     { return val, true }
    // KEY_ABSENT → keep going to the next, older table
}
return nil, false
```

The `KEY_DELETED` early return is what makes deletes work across levels. Hitting a tombstone
means the newest record for this key is a delete, so searching older tables would be wrong —
they'd return the pre-delete value.

Each `Lookup` checks that table's bloom filter first, so most of these iterations cost no disk
I/O at all.

## `flushMemtable`

```go
db.mu.Lock(); defer db.mu.Unlock()

sst, err := sstable.Flush(db.memtable, db.conf.SSTDir)   // writes + fsyncs the file
db.sstables = append([]*sstable.SSTable{sst}, db.sstables...)   // PREPEND — newest first
db.memtable = memtable.NewMemTable()

db.resetWAL()                                            // log is now redundant

compacted, err := compaction.Compact(db.sstables, db.conf.SSTDir)
db.sstables = compacted
```

The ordering here is the crash-safety argument:

1. **Write and fsync the SSTable first.** Until the file is durable, the WAL is the only copy.
2. **Prepend, don't append.** This is what maintains the newest-first invariant.
3. **Only then reset the WAL.** Truncating before the fsync would lose data on a crash.
4. **Compact last**, with the new table already in the list.

A crash between steps 1 and 3 leaves both an SSTable and a WAL containing the same data —
harmless, since replay just re-applies writes that are already on disk, and the replayed
memtable shadows the identical SSTable values.

`ForceFlush()` is a public wrapper over `flushMemtable` for tests and benchmarks.

`resetWAL` (`db.go`):
```go
db.wal.File.Truncate(0)
db.wal.File.Seek(0, 0)
db.wal.BufWriter.Reset(db.wal.File)   // drop buffered bytes that would survive the truncate
```

## `Close` / `PrintMemTable`

`Close()` is just `db.wal.Sync()` — flush the buffer and fsync. It deliberately does **not**
flush the memtable: the WAL is sufficient, and replay will rebuild it on the next `Open`. That's
the whole point of having a WAL.

`PrintMemTable()` is a debug dump via `memtable.Ascend`, printing `[tombstone]` for deletes.

## Full data flow

```
Put(k,v) ──► WAL append (fsync every 10) ──► MemTable (B-tree, sorted)
                                                  │
                                       Size() >= 4MB
                                                  ▼
                                    sstable.Flush → <UnixNano>.sst  (fsync)
                                                  │
                                       prepend to db.sstables
                                       reset WAL
                                                  ▼
                                    Compact if >= 4 tables (size-tiered)

Get(k)  ──► MemTable ──miss──► sst[0] ──► sst[1] ──► ... (newest → oldest)
                                 │
                        each: bloom → index binary search → read 1 block → scan
                        first KEY_FOUND or KEY_DELETED wins
```

## Concurrency

- `db.mu` is an `RWMutex`. `Get` takes the read lock; `flushMemtable` takes the write lock.
- `Put` and `Delete` take **no** `db.mu` lock at all. They mutate the WAL (which has no internal
  locking) and the memtable (which does have its own `RWMutex`).
- So: concurrent readers are fine, and readers vs. flush is correctly serialised, but
  **concurrent writers race on the WAL** — `putCounter` and the shared `bufio.Writer` are
  unprotected. Interleaved `Append` calls can produce a corrupt log.
- `Put` can also call `flushMemtable`, which takes the write lock, while the caller holds
  nothing — so two simultaneous writers can both decide to flush.

Single-writer usage is safe. Multi-writer is not.

## Known limitations

- **`Delete` never triggers a flush.** Only `Put` checks `Size()`. A delete-heavy workload grows
  the memtable and WAL without bound.
- **Writes aren't locked** — see Concurrency above.
- **Flush and compaction are synchronous**, holding the write lock. All reads and writes stall
  for the duration of a file write plus a possible multi-table merge.
- **No immutable memtable / no background flush.**
- **`searchSSTables` swallows errors** — a read error (corrupt block, missing file) is reported
  as a plain "not found", indistinguishable from a genuine miss.
- **Recency ordering is positional, not recorded.** `db.sstables` order is maintained by
  prepending, but `compaction.Compact` can return a slice whose order no longer reflects
  recency (see [compaction.md](compaction.md)). There is no manifest to recover the true order.
- **No iterators / range scans** on `DB` — the components support ordered iteration, but it
  isn't exposed.

## Related

- [wal.md](wal.md) · [memtable.md](memtable.md) · [sstable.md](sstable.md) ·
  [compaction.md](compaction.md) · [bloomfilter.md](bloomfilter.md) · [config.md](config.md)
