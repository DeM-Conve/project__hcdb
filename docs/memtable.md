# MemTable

Package: `memtable/` — files: `memtable_types.go`, `memtable.go`, `iterator.go`

## Why it exists

The memtable is the in-memory write buffer. Every write lands here (after the WAL) and stays
until the table gets big enough to be flushed to disk as an SSTable.

Its job is to **turn random writes into sequential writes**. Callers insert keys in arbitrary
order; the memtable keeps them sorted, so when we flush we can stream the whole thing out to
disk in one sorted sequential pass — which is what makes the SSTable's block index and binary
search possible.

## Types

```go
type Item struct {
    Type  uint8   // 0 = put, 1 = tombstone
    Key   []byte
    Value []byte
}

type MemTable struct {
    mut  sync.RWMutex
    tree *btree.BTree
    size int          // running byte estimate of live data
}
```

## Data structure: B-tree, not a skiplist

We use `github.com/google/btree` with degree `config.DEFAULT_BTREE_DEGREE` (= 32).

Ordering comes from the `btree.Item` interface — one method:

```go
func (a Item) Less(b btree.Item) bool {
    return string(a.Key) < string(b.(Item).Key)
}
```

Go's `string` comparison on a `[]byte` conversion is bytewise lexicographic, which is the same
total order the SSTable index binary search (`bytes.Compare`) assumes. **These two must agree** —
if they ever diverge, index lookups silently miss keys.

Why a B-tree rather than the skiplist most LSM engines use: it gives sorted iteration and
O(log n) point lookups with a well-tested off-the-shelf library, and it's cache-friendlier at
degree 32 (each node holds up to 63 items, so a lookup touches few nodes). The classic reason to
prefer a skiplist — lock-free concurrent inserts — doesn't apply here because we serialise
everything behind a mutex anyway.

## Operations

All five methods take the lock: `Put`/`Delete` take the write lock, `Get`/`Size`/`Ascend` take
the read lock. `RWMutex` means concurrent reads don't block each other.

### `Put(key, value)`

```go
newItem := Item{Key: key, Value: value, Type: 0}

old := tree.Get(newItem)
if old != nil {
    size -= len(oldItem.Key) + len(oldItem.Value)   // un-count the version being replaced
}
tree.ReplaceOrInsert(newItem)
size += len(newItem.Key) + len(newItem.Value)
```

The lookup-before-insert exists purely for **size accounting**. `ReplaceOrInsert` overwrites in
place, so without subtracting the old entry's bytes first, overwriting the same key 1000 times
would inflate `size` 1000× and trigger a flush of a table that is actually tiny.

### `Get(key)`

```go
result := tree.Get(Item{Key: key})   // only Key matters — Less() ignores the rest
if result == nil          { return nil, false }
if item.Type == OP_DELETE { return nil, false }   // tombstone
return item.Value, true
```

Two distinct "not found" cases collapse into the same `(nil, false)` return:

1. **Not in this memtable** — the caller (`db.Get`) should keep looking in the SSTables.
2. **Tombstoned here** — the key is deleted; the caller should *stop* looking.

These are conflated. `db.Get` falls through to `searchSSTables` in both cases. It happens to
still be correct, because the SSTable layer has its own tombstone handling (`KEY_DELETED`) and
the flushed SSTable containing the tombstone is searched newest-first — but only when the
tombstone has already been flushed. The SSTable layer models this properly with a three-valued
`KEY_LOOKUP_ENUM`; the memtable does not.

### `Delete(key)`

Deletes do not remove anything. They insert a **tombstone**:

```go
tombstone := Item{Key: key, Value: nil, Type: 1}
tree.ReplaceOrInsert(tombstone)
size += len(tombstone.Key)   // key only — nil value contributes 0
```

Why: the key may exist in an SSTable on disk. Removing it from the memtable would just make the
old on-disk value visible again. The tombstone is a *newer* record that shadows it, and it must
be flushed to disk and propagate through compaction before the key is truly gone.

Note that a `Delete` of a key that was never present still inserts a tombstone and still grows
`size`.

### `Size()`

Returns the running byte estimate: `sum(len(key) + len(value))` over live items. It counts
**payload bytes only** — no B-tree node overhead, no per-entry framing (the 9 bytes of
type + keyLen + valLen that the SSTable block format adds). So real memory use is meaningfully
higher than `Size()` reports, and the flushed SSTable is larger than `Size()` bytes.

`db.Put` compares this against `DEFAULT_MEMTABLE_FLUSH_SIZE` (4 MB) to decide when to flush.

### `Ascend(fn)`

In-order traversal, the whole reason the tree is sorted:

```go
memTable.Ascend(func(key, value []byte, itemType uint8) bool { ... })
```

Returning `false` from the callback stops iteration early. It hands the caller the raw
`(key, value, type)` triple rather than leaking the `Item` type or the `btree` dependency.

Two consumers:
- `sstable.Flush` — streams entries out in sorted order to build blocks and the index.
- `db.PrintMemTable` — debug dump, prints `[tombstone]` for deleted keys.

`Ascend` holds the **read** lock for its entire duration. `sstable.Flush` runs inside it, so a
flush blocks all writers until the file is fully written and `fsync`'d.

### `Iterator` (`iterator.go`) — one source in the range-scan merge

```go
func NewIterator(mt *MemTable, lowerBound, upperBound []byte) *Iterator
```

Unlike `sstable.Iterator` (see [sstable.md](sstable.md)), this one is **eager**, not lazy: it
calls `mt.tree.AscendGreaterOrEqual(pivot, ...)` once, up front, and copies every matching
`Item` into a plain `[]Item` slice, stopping the moment it passes `upperBound`. `Next()` then
just walks that slice with a position index. This is safe specifically because the memtable is
already bounded, in-memory data — there's no disk cost to defer the way there is for an
SSTable's blocks, so eagerly snapshotting the range is simpler and just as cheap.

Exists purely to satisfy `db`'s `source` interface (`Valid`/`Key`/`Value`/`Type`/`Next`) so
`db.Scan`'s k-way merge can treat the memtable and every SSTable identically — see
[db.md](db.md) for the merge itself.

## Lifecycle

```
db.Open      → NewMemTable(), then WAL replay re-applies every entry into it
db.Put       → wal.Put, then memtable.Put, then check Size() >= 4MB
flush        → sstable.Flush(memtable) → memtable = NewMemTable() → WAL truncated
```

The old memtable is simply dropped and garbage-collected. There is no immutable-memtable
handoff and no background flush — flush is synchronous and blocking.

## Known limitations

- **Flush is only checked on `Put`.** `db.Delete` never checks `Size()`, so a delete-only
  workload grows the memtable (and the WAL) without bound.
- **No immutable memtable.** Writes stall for the full duration of a flush + compaction.
- **`Size()` undercounts.** Payload bytes only; the real footprint is larger.
- **Tombstone vs. absent is not distinguishable** to the caller of `Get`.

## Related

- [wal.md](wal.md) — durability; what refills the memtable on restart
- [sstable.md](sstable.md) — where `Ascend` output goes
- [db.md](db.md) — flush trigger, read ordering, and `Scan`'s use of `Iterator`
- [config.md](config.md) — `DEFAULT_BTREE_DEGREE`, `DEFAULT_MEMTABLE_FLUSH_SIZE`
