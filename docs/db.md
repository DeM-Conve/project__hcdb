# DB — the orchestration layer

Package: `db/` — files: `types.go`, `db.go`, `operations.go`, `iterator.go`, `db_test.go`,
`operations_test.go`, `snapshot_test.go`

## Why it exists

`db` is the public API and the only place that knows about all the other packages. Everything
below it — WAL, memtable, SSTable, bloom filter, compaction — is a component that doesn't know
the others exist. `db` wires them into an LSM tree and owns the rules that make it correct:

1. **Write order**: WAL before memtable.
2. **Read order**: memtable, then SSTables newest → oldest, stopping at the first answer.
3. **Every write gets its own sequence number** (`db.seqNum`), which is what MVCC snapshots and
   compaction's tombstone-safety logic are both built on.

## Type

```go
type DB struct {
    mu         sync.RWMutex
    wal        *wal.WAL
    memtable   *memtable.MemTable
    sstables   []*sstable.SSTable   // INVARIANT: newest first
    conf       config.Config
    blockCache cache.Cacher
    seqNum     atomic.Uint64        // last assigned sequence number
    watermark  *base.Watermark      // active snapshots — see GetSnapshot/ReleaseSnapshot
}
```

`sstables` being ordered newest-first is the single most important invariant in the codebase.
Every read depends on it — it's what makes a newer value shadow an older one, and a tombstone
shadow a live value.

`blockCache` is a `cache.Cacher` (a `*cache.ShardedLRU` in practice, see [cache.md](cache.md)),
not a bare `*cache.LRU` — the field is typed as the interface so `db` doesn't care which
implementation backs it.

## Internal keys: every write is versioned

Nothing below `db` ever sees a raw user key. `db.encodeNextKey` turns each `Put`/`Delete`'s user
key into an **internal key** — the user key plus an 8-byte trailer packing a fresh sequence
number (`db.seqNum.Add(1)`) and a kind (`base.InternalKeyKindSet` / `InternalKeyKindDelete`):

```go
func (db *DB) encodeNextKey(userKey []byte, kind base.InternalKeyKind) []byte {
    seqNum := base.SeqNum(db.seqNum.Add(1))
    k := base.MakeInternalKey(userKey, seqNum, kind)
    buf := make([]byte, k.Size())
    k.Encode(buf)
    return buf
}
```

That encoded key — not the raw string — is what goes to the WAL, the memtable and eventually an
SSTable. Two consequences:

- **Repeated writes to the same user key don't overwrite each other in place.** Each carries a
  distinct sequence number, so old versions stay retrievable until compaction proves no snapshot
  still needs them (see [compaction.md](compaction.md)).
- **Internal keys sort by user key ascending, then sequence number descending**
  (`base.InternalCompare`), so the newest version of a key is always the first one a forward scan
  encounters — which is what every read path below relies on.

See [Snapshots and `GetAt` / `ScanAt`](#snapshots-and-getat--scanat) for what this makes possible.

## `Open(conf)`

```go
os.MkdirAll(conf.SSTDir, 0755)
walObj, _        := wal.Open(conf.WALPath)
mem, walMaxSeq,_ := rebuildMemtable(walObj)          // replay the log
tables, _        := sstable.OpenAllInDir(conf.SSTDir)

blockCache := cache.NewShardedLRU(
    config.DEFAULT_CACHE_SHARD_COUNT,
    config.DEFAULT_BLOCK_CACHE_ENTRIES/config.DEFAULT_CACHE_SHARD_COUNT,
)
for _, sst := range tables {
    sst.SetCache(blockCache)
}

sstMaxSeq, _ := maxSeqNumInTables(tables)
db := &DB{wal: walObj, memtable: mem, sstables: tables, conf: conf,
    blockCache: blockCache, watermark: base.NewWatermark()}
db.seqNum.Store(max(uint64(walMaxSeq), uint64(sstMaxSeq)))
return db, nil
```

`rebuildMemtable` calls `walObj.Replay()` and applies each entry by decoding its key's kind:

```go
if base.DecodeInternalKey(e.Key).Kind() == base.InternalKeyKindDelete {
    mem.Delete(e.Key)
} else {
    mem.Put(e.Key, e.Value)
}
```

Replay is in write order, so later writes' internal keys sort ahead of earlier ones for the same
user key and the memtable ends up in exactly the state it had before the crash — minus whatever
was lost to the WAL's 10-write sync window, and minus any torn tail record that replay truncated.
While replaying, `rebuildMemtable` also tracks the highest sequence number it sees, returned as
`walMaxSeq`.

`OpenAllInDir` sorts filenames descending (they're `UnixNano` timestamps), which establishes the
newest-first invariant at startup.

`maxSeqNumInTables` does a full `sstable.NewBlockIterator` pass over every SSTable to find the
highest sequence number already durable on disk — needed because the WAL is truncated after every
flush (see `resetWAL` below), so on restart it no longer carries the sequence numbers of anything
already flushed. `db.seqNum` resumes from `max(walMaxSeq, sstMaxSeq)` so a freshly assigned
sequence number can never collide with one already written. This is an `O(data)` scan on every
`Open`; a manifest recording the last sequence number (as LevelDB does) would make it O(1) — see
Known limitations.

## `Put(key, value)` (`operations.go`)

```go
if len(key) > config.MAX_KEY_LENGTH     { return ErrKeyTooLong }
if len(value) > config.MAX_VALUE_LENGTH { return ErrValueTooLong }

db.mu.Lock(); defer db.mu.Unlock()

internalKey := db.encodeNextKey([]byte(key), base.InternalKeyKindSet)
if err := db.wal.Put(internalKey, []byte(value)); err != nil { return err }   // durability FIRST
db.memtable.Put(internalKey, []byte(value))

return db.flushIfFullLocked()
```

Key/value length is validated **before anything else, and before the lock**. The WAL enforces
those same limits again on `Replay` (against the encoded key length there, since that's what's
actually on disk — see [wal.md](wal.md)), where an over-long record is read as corruption and the
log is truncated there — so accepting an oversized write at `Put` time used to return `nil` to
the caller and then silently discard that record *and every record after it* on the next restart.
`ErrKeyTooLong`/`ErrValueTooLong` (defined in `operations.go`) close that gap. Regression test:
`TestOversizedWriteRejected`.

The WAL write comes first and its error short-circuits, so the memtable can never contain a
write that isn't in the log. If it were the other way round, a crash between the two steps would
lose an acknowledged write.

`Put` holds `db.mu.Lock()` for the **entire call**, including the flush it may trigger — see
[Concurrency](#concurrency) for why, and for what changed here.

## `Delete(key)`

```go
if len(key) > config.MAX_KEY_LENGTH { return ErrKeyTooLong }

db.mu.Lock(); defer db.mu.Unlock()

internalKey := db.encodeNextKey([]byte(key), base.InternalKeyKindDelete)
if err := db.wal.Delete(internalKey); err != nil { return err }
db.memtable.Delete(internalKey)

return db.flushIfFullLocked()
```

Same ordering, same locking, same length validation on the key (a tombstone carries no value, so
there's nothing to check `MAX_VALUE_LENGTH` against). Writes a tombstone in both places, then
runs the same `flushIfFullLocked` check as `Put` — tombstones are ordinary memtable entries under
MVCC, so a delete-only workload has to be able to cross the flush threshold too.
Regression test: `TestDeleteTriggersFlush`.

`flushIfFullLocked` is the shared tail both `Put` and `Delete` call into:

```go
func (db *DB) flushIfFullLocked() error {
    if db.memtable.Size() < config.DEFAULT_MEMTABLE_FLUSH_SIZE {
        return nil
    }
    return db.flushMemtableLocked()
}
```

It exists as its own function specifically so both callers already holding `db.mu` can reach the
flush path without a second lock acquisition — see `flushMemtableLocked`'s doc comment under
Concurrency.

## Snapshots and `GetAt` / `ScanAt`

```go
func (db *DB) GetSnapshot() base.SeqNum {
    snap := base.SeqNum(db.seqNum.Load())
    db.watermark.Begin(snap)
    return snap
}

func (db *DB) ReleaseSnapshot(snap base.SeqNum) {
    db.watermark.Done(snap)
}
```

`GetSnapshot` reads the current sequence number and pins it in `db.watermark` (a
`*base.Watermark`, `internal/base/watermark.go`) — a small refcounted map from pinned sequence
number to how many callers currently hold it, so the same snapshot value obtained twice (or
released twice) is tracked correctly. Every read taken "as of" that snapshot then only needs to
compare sequence numbers; nothing about the read path itself changes. `ReleaseSnapshot` must be
called once per `GetSnapshot`, or that version — and everything it shadows — stays pinned and
un-collectible by compaction forever (see `Watermark.Floor` below and
[compaction.md](compaction.md)).

`Get` and `Scan` are now thin wrappers over the snapshot-aware versions, passing `base.SeqNumMax`
(the sentinel meaning "every version, i.e. the latest"):

```go
func (db *DB) Get(key string) ([]byte, bool) { return db.GetAt(key, base.SeqNumMax) }
```

### `GetAt(key, snapshot)`

```go
db.mu.RLock(); defer db.mu.RUnlock()

switch val, st := db.memtable.Get([]byte(key), snapshot); st {
case base.KEY_FOUND:   return val, true    // memtable is always the freshest
case base.KEY_DELETED: return nil, false   // STOP — deleted
}
return db.searchSSTables([]byte(key), snapshot)   // KEY_ABSENT → try the SSTables
```

Structurally unchanged from before snapshots existed — the only difference is `snapshot` is
threaded through instead of implicitly being "latest everywhere". `memtable.Get` and
`sstable.Lookup` both take it and do their own visibility filtering (see
[memtable.md](memtable.md), [sstable.md](sstable.md)); `db` itself needs no snapshot-specific
logic beyond passing the value down.

### `searchSSTables(key, snapshot)`

```go
for _, sst := range db.sstables {                  // newest → oldest
    val, st, err := sst.Lookup(key, snapshot)
    if err != nil               { return nil, false }
    if st == base.KEY_DELETED   { return nil, false }   // STOP — deleted
    if st == base.KEY_FOUND     { return val, true }
    // KEY_ABSENT → keep going to the next, older table
}
return nil, false
```

Every level answers with the same three-valued `base.KEY_LOOKUP_ENUM`, and the `KEY_DELETED`
early return at each one is what makes deletes work across levels. Hitting a tombstone means the
newest *visible* record for this key is a delete, so searching older levels would be wrong —
they'd return the pre-delete value.

The memtable is a level like any other here. Both it and every SSTable share the three-valued
result specifically so a tombstone can stop the search — collapsing "absent" and "deleted" into
one result (as a plain `bool` would) lets a delete recorded at one level get skipped, resurfacing
an older value from a level below it.

Each `Lookup` checks that table's bloom filter first, so most of these iterations cost no disk
I/O at all.

### `ScanAt(lowerBound, upperBound, snapshot)`

`Scan` is `ScanAt(lowerBound, upperBound, base.SeqNumMax)`. Both build the same
`MergeIterator`, just threading `snapshot` down to each source (`memtable.NewIterator` and
`sstable.NewIterator` both take it and filter internally) — the merge logic in `iterator.go`
itself needs no awareness of snapshots at all, since every entry it ever sees from any source is
already guaranteed visible. See [The merge](#the-merge-a-min-heap-over-sources) below.

Regression tests for the snapshot machinery live in `db/snapshot_test.go`:
`TestSnapshotIsolation`, `TestSnapshotSeesDeletesCorrectly`, `TestSnapshotAcrossFlush` (a snapshot
taken before a flush still sees the pre-flush value once the data has moved from memtable to
SSTable), `TestSnapshotSpansMemtableAndSSTable`, `TestScanAtSnapshot`, and
`TestScanAtSnapshotAfterFlush`.

## `flushMemtableLocked`

```go
// caller must already hold db.mu for writing
func (db *DB) flushMemtableLocked() error {
    sst, err := sstable.Flush(db.memtable, db.conf.SSTDir)   // writes, fsyncs, atomically installs
    sst.SetCache(db.blockCache)

    db.sstables = append([]*sstable.SSTable{sst}, db.sstables...)   // PREPEND — newest first
    db.memtable = memtable.NewMemTable()

    db.resetWAL()                                            // log is now redundant

    compacted, err := compaction.Compact(db.sstables, db.conf.SSTDir, db.watermark.Floor())
    for _, sst := range compacted {
        sst.SetCache(db.blockCache)
    }
    db.sstables = compacted
    return nil
}
```

`sstable.Flush` itself writes to a temp file and installs it atomically (rename + directory
fsync) rather than writing directly to the final path — see [sstable.md](sstable.md#atomic-install-atomic_installgo)
and [faultinjection.md](faultinjection.md). Every SSTable that enters `db.sstables`, from either
`Open` or `flushMemtableLocked`, gets `SetCache(db.blockCache)` called on it — the cache is one
shared instance for the whole `DB`, not one per SSTable (see [cache.md](cache.md)).

`db.watermark.Floor()` is passed straight to `compaction.Compact`: the oldest sequence number any
live `GetSnapshot` snapshot still needs, so compaction knows which superseded versions are still
unsafe to drop (see [compaction.md](compaction.md)).

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

`ForceFlush()` is a public wrapper over `flushMemtableLocked`, taking `db.mu.Lock()` itself, for
tests and benchmarks that need to flush without going through `Put`/`Delete`'s size check.

`resetWAL` (`db.go`):
```go
db.wal.File.Truncate(0)
db.wal.File.Seek(0, 0)
db.wal.BufWriter.Reset(db.wal.File)   // drop buffered bytes that would survive the truncate
```

## `Scan` / `ScanAt` (`iterator.go`)

Range scans across the memtable and every SSTable, merged in sorted order, newest-wins on
duplicate keys, tombstones hidden, restricted to versions visible at a snapshot.

### `source` interface

```go
type source interface {
    Valid() bool
    Key() []byte    // encoded internal key
    Value() []byte
    Next()
}
```

There's no `Type()` method — kind is decoded from the internal key itself
(`base.DecodeInternalKey(key).Kind()`), not carried as a separate field. `*memtable.Iterator` and
`*sstable.Iterator` both satisfy this — structurally, with no `implements` declaration anywhere —
so the merge logic below treats "the memtable" and "an SSTable" identically. Both take a
`snapshot base.SeqNum` at construction and only ever yield entries visible at it; see
[memtable.md](memtable.md) and [sstable.md](sstable.md) for how each one actually walks its data
(eager snapshot vs. lazy block-at-a-time).

### The merge: a min-heap over sources

```go
type mergeItem struct {
    key      []byte   // encoded internal key
    priority int       // lower = newer; wins ties on identical internal keys
    src      source
}

type mergeHeap []*mergeItem   // implements container/heap.Interface: Len, Less, Swap, Push, Pop
```

`ScanAt` builds one iterator per source (each already filtered to `snapshot`), pushes each onto
the heap (skipping any source with nothing in range), and returns a `*MergeIterator`. `priority`
is `0` for the memtable (always freshest) and `i+1` for `db.sstables[i]` — which is already
newest-first, so this directly reuses the same ordering invariant the rest of `db` depends on.
`Scan` is `ScanAt(lowerBound, upperBound, base.SeqNumMax)`.

`mergeHeap.Less` orders by **internal key**: `base.InternalCompare` gives user key ascending,
then sequence number descending, so the newest version of a key pops first; `priority` only
breaks ties between two sources holding the literal same internal key (e.g. a replayed memtable
entry and the SSTable it was also flushed to).

`Next()`, each call:

1. `heap.Pop` — the smallest internal key across every source; decode it to get the user key and
   kind, and read the winner's value before advancing (`Value()` before `Next()`, since advancing
   can invalidate the block a decoded slice points into — see [sstable.md](sstable.md)).
2. **`skipPast`**: advance the popped source past every remaining entry for that same user key,
   re-pushing it if it still has data — this is what implements newest-wins, and it applies both
   to other sources holding older versions of the key *and* to further, older versions sitting
   inside the very same source's own remaining entries (one source can hold several versions of
   one user key). Repeat while the new heap root still shares the user key.
3. If the key is past `upperBound`, stop.
4. If the winning entry's kind is `base.InternalKeyKindDelete`, skip it — loop back to step 1
   instead of returning.
5. Otherwise, save the user key and value as the current position and return `true`.

### Concurrency gap

`Scan` takes `db.mu.RLock()` only long enough to build the initial heap, then releases it —
`Next()` calls happen with no lock held at all. Each `sstable.Iterator` holds a `FilePath` and
re-`os.Open`s it per block. If `compaction.Compact` runs concurrently and deletes an SSTable a
scan is still iterating, that scan's next block read will fail. Real engines solve this with
reference counting so a file isn't deleted while an iterator still holds it open; HCDB does not
yet.

## `Close` / `PrintMemTable`

`Close()` is just `db.wal.Sync()` — flush the buffer and fsync. It deliberately does **not**
flush the memtable: the WAL is sufficient, and replay will rebuild it on the next `Open`. That's
the whole point of having a WAL.

`PrintMemTable()` is a debug dump via `memtable.Ascend`, printing `[tombstone]` for deletes.

## Full data flow

```
Put(k,v) ──► encodeNextKey (seqNum++) ──► WAL append (fsync every 10) ──► MemTable (B-tree)
                                                                                │
                                                                     Size() >= 4MB
                                                                                ▼
                                                          sstable.Flush → <UnixNano>.sst  (fsync)
                                                                                │
                                                                     prepend to db.sstables
                                                                     reset WAL
                                                                                ▼
                                                    Compact(floor=watermark.Floor()) if >= 4 tables

Get(k) = GetAt(k, SeqNumMax) ──► MemTable ──miss──► sst[0] ──► sst[1] ──► ... (newest → oldest)
                                     │
                            each: visible-at-snapshot check → bloom → seek → read 1 block → scan
                            first KEY_FOUND or KEY_DELETED (at or below snapshot) wins
```

## Concurrency

`db.mu` is an `RWMutex` guarding every entry point:

| caller | lock |
|---|---|
| `GetAt`, `ScanAt`, `PrintMemTable` | `RLock` |
| `Put`, `Delete`, `ForceFlush`, `Close` | `Lock` |
| `GetSnapshot`, `ReleaseSnapshot` | none of `db.mu` — see below |

Writers take the **write** lock, not the read lock, for two reasons: the WAL's `bufio.Writer`
and `putCounter` have no internal locking of their own, and the sequence number has to reach
the log in assignment order. Unsynchronised writers interleave mid-record and corrupt the file
on disk — `go test -race` reported 13 distinct races on four concurrent `Put`s before this was
locked.

Because `Put` holds `db.mu` and may need to flush, the flush path is split in two:

```go
func (db *DB) ForceFlush() error {
    db.mu.Lock(); defer db.mu.Unlock()
    return db.flushMemtableLocked()
}

// caller must already hold db.mu for writing
func (db *DB) flushMemtableLocked() error { ... }
```

`flushMemtableLocked` must not take the lock itself. Go mutexes are **not reentrant**, so a
`Put` that already holds `db.mu` and then calls a function which locks it again deadlocks
immediately. This is the same shape as `cache.evictOldest`, which is called under `insert`'s
lock for the same reason.

Regression test: `TestConcurrentWriters` in `operations_test.go` (run with `-race`).

`GetSnapshot`/`ReleaseSnapshot` don't take `db.mu` at all — `db.seqNum.Load()` is a lock-free
atomic read, and `base.Watermark` guards its own map with its own `sync.Mutex` (see
`internal/base/watermark.go`). That's safe precisely because a snapshot is just a number plus a
pin on that number; it doesn't need to observe `db.memtable`/`db.sstables` atomically the way a
read or a flush does.

Still not safe: a `Scan`/`ScanAt` iterator outlives the `RLock` taken to build it — see the
Concurrency gap above.

## Known limitations

- **Flush and compaction are synchronous**, holding the write lock. All reads and writes stall
  for the duration of a file write plus a possible multi-table merge.
- **No immutable memtable / no background flush.**
- **`searchSSTables` swallows errors** — a read error (corrupt block, missing file) is reported
  as a plain "not found", indistinguishable from a genuine miss.
- **Recency ordering is positional, not recorded.** `db.sstables` order is maintained by
  prepending, but `compaction.Compact` can return a slice whose order no longer reflects
  recency (see [compaction.md](compaction.md)). There is no manifest to recover the true order.
- **`Scan`/`ScanAt` isn't concurrency-safe against compaction** — see the Concurrency gap under
  `Scan` above. Correct for single-threaded use, not for a scan running alongside a flush. A
  long-lived snapshot mitigates the *data* half of this (compaction keeps what the snapshot
  needs) but not the *file-handle* half — a scan's `sstable.Iterator` can still get a hard read
  error if the specific file it has open is deleted mid-scan, since nothing pins open files
  against concurrent deletion.
- **A held snapshot blocks tombstone (and superseded-version) reclamation indefinitely.** Since
  `Watermark.Floor()` is the minimum of every pinned sequence number, one long-lived
  `GetSnapshot` caller that forgets to call `ReleaseSnapshot` prevents compaction from dropping
  anything that snapshot might still need — an unbounded resource leak from the database's
  point of view, not just a client-side handle leak.
- **`maxSeqNumInTables` is an O(data) scan on every `Open`.** No manifest records the last
  sequence number, so restart has to re-derive it by reading every SSTable in full.

## Related

- [wal.md](wal.md) · [memtable.md](memtable.md) · [sstable.md](sstable.md) ·
  [compaction.md](compaction.md) · [bloomfilter.md](bloomfilter.md) · [cache.md](cache.md) ·
  [faultinjection.md](faultinjection.md) · [config.md](config.md) · [resp.md](resp.md) ·
  [server.md](server.md) · [deployment.md](deployment.md)
