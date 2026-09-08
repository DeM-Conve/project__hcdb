# DB — the orchestration layer

Package: `db/` — files: `types.go`, `db.go`, `operations.go`, `iterator.go`, `db_test.go`

## Why it exists

`db` is the public API and the only place that knows about all the other packages. Everything
below it — WAL, memtable, SSTable, bloom filter, compaction — is a component that doesn't know
the others exist. `db` wires them into an LSM tree and owns the two rules that make it correct:

1. **Write order**: WAL before memtable.
2. **Read order**: memtable, then SSTables newest → oldest, stopping at the first answer.

## Type

```go
type DB struct {
    mu         sync.RWMutex
    wal        *wal.WAL
    memtable   *memtable.MemTable
    sstables   []*sstable.SSTable   // INVARIANT: newest first
    conf       config.Config
    blockCache *cache.LRU
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

blockCache := cache.NewLRU(config.DEFAULT_BLOCK_CACHE_ENTRIES)
for _, sst := range tables {
    sst.SetCache(blockCache)
}
return &DB{wal, mem, tables, conf, blockCache}
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

sst, err := sstable.Flush(db.memtable, db.conf.SSTDir)   // writes, fsyncs, atomically installs
sst.SetCache(db.blockCache)
db.sstables = append([]*sstable.SSTable{sst}, db.sstables...)   // PREPEND — newest first
db.memtable = memtable.NewMemTable()

db.resetWAL()                                            // log is now redundant

compacted, err := compaction.Compact(db.sstables, db.conf.SSTDir)
for _, sst := range compacted {
    sst.SetCache(db.blockCache)
}
db.sstables = compacted
```

`sstable.Flush` itself now writes to a temp file and installs it atomically (rename + directory
fsync) rather than writing directly to the final path — see [sstable.md](sstable.md#atomic-install-atomic_installgo)
and [faultinjection.md](faultinjection.md). Every SSTable that enters `db.sstables`, from either
`Open` or `flushMemtable`, gets `SetCache(db.blockCache)` called on it — the cache is one shared
instance for the whole `DB`, not one per SSTable (see [cache.md](cache.md)).

The ordering here is the crash-safety argument:

1. **Write and fsync the SSTable first.** Until the file is durable, the WAL is the only copy.
2. **Prepend, don't append.** This is what maintains the newest-first invariant.
3. **Only then reset the WAL.** Truncating before the fsync would lose data on a crash.
4. **Compact last**, with the new table already in the list.

A crash between steps 1 and 3 leaves both an SSTable and a WAL containing the same data —
harmless, since replay just re-applies writes that are already on disk, and the replayed
memtable shadows the identical SSTable values. `TestCrashBetweenFlushAndWALReset` (`db_test.go`)
proves this directly: it calls `sstable.Flush` on its own, deliberately skips `resetWAL()`, then
reopens a fresh `DB` over the same paths to simulate a restart, and asserts both that reads are
still correct *and* that the redundancy (data in both the replayed memtable and the installed
SSTable) is real, not just assumed. See [faultinjection.md](faultinjection.md) for the full
reasoning chain that led here.

`ForceFlush()` is a public wrapper over `flushMemtable` for tests and benchmarks.

`resetWAL` (`db.go`):
```go
db.wal.File.Truncate(0)
db.wal.File.Seek(0, 0)
db.wal.BufWriter.Reset(db.wal.File)   // drop buffered bytes that would survive the truncate
```

## `Scan(lowerBound, upperBound)` (`iterator.go`)

Range scans across the memtable and every SSTable, merged in sorted order, newest-wins on
duplicate keys, tombstones hidden. This is the k-way merge the earlier "No iterators" limitation
used to name as missing.

### `source` interface

```go
type source interface {
    Valid() bool
    Key() []byte
    Value() []byte
    Type() uint8
    Next()
}
```

`*memtable.Iterator` and `*sstable.Iterator` both satisfy this — structurally, with no
`implements` declaration anywhere — so the merge logic below treats "the memtable" and "an
SSTable" identically. See [memtable.md](memtable.md) and [sstable.md](sstable.md) for how each
one actually walks its data (eager snapshot vs. lazy block-at-a-time).

### The merge: a min-heap over sources

```go
type mergeItem struct {
    key      []byte
    priority int   // lower = newer; wins ties on duplicate keys
    src      source
}

type mergeHeap []*mergeItem   // implements container/heap.Interface: Len, Less, Swap, Push, Pop
```

`Scan` builds one iterator per source, pushes each onto the heap (skipping any source with
nothing in range), and returns a `*MergeIterator`. `priority` is `0` for the memtable (always
freshest) and `i+1` for `db.sstables[i]` — which is already newest-first, so this directly reuses
the same ordering invariant the rest of `db` depends on.

`Next()`, each call:

1. `heap.Pop` — the smallest key across every source.
2. **Duplicate-key handling**: while the new heap root has the *same* key, pop and discard it
   too, advancing that (older, by the `priority` tie-break in `Less`) source past the key without
   emitting it. This is what implements newest-wins.
3. Read the winner's value/type, advance its source, re-push if it still has more.
4. If the key is past `upperBound`, stop.
5. If the entry is a tombstone (`config.OP_DELETE`), skip it — loop back to step 1 instead of
   returning.
6. Otherwise, save it as the current position and return `true`.

### Concurrency gap

`Scan` takes `db.mu.RLock()` only long enough to build the initial heap, then releases it —
`Next()` calls happen with no lock held at all. Each `sstable.Iterator` holds a `FilePath` and
re-`os.Open`s it per block. If `compaction.Compact` runs concurrently and deletes an SSTable a
scan is still iterating, that scan's next block read will fail. Real engines solve this with
reference counting so a file isn't deleted while an iterator still holds it open; hcdb does not
yet.

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
- **`Scan` isn't concurrency-safe against compaction** — see the Concurrency gap under `Scan`
  above. Correct for single-threaded use, not for a scan running alongside a flush.

## Related

- [wal.md](wal.md) · [memtable.md](memtable.md) · [sstable.md](sstable.md) ·
  [compaction.md](compaction.md) · [bloomfilter.md](bloomfilter.md) · [cache.md](cache.md) ·
  [faultinjection.md](faultinjection.md) · [config.md](config.md)
