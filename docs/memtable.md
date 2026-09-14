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
    Key   []byte // encoded internal key; its trailer carries the kind
    Value []byte
}

type MemTable struct {
    mut  sync.RWMutex
    tree *btree.BTree
    size int          // running byte estimate of live data
}
```

`Item.Key` is not a raw user key — it's an **encoded internal key**: the user key plus an 8-byte
trailer packing a sequence number and a kind (`base.InternalKeyKindSet` /
`base.InternalKeyKindDelete`, see `internal/base/internal.go`). There is no separate `Type`
field any more; put-vs-delete lives entirely in the trailer. Every write gets its own sequence
number (`db.encodeNextKey`), so a key written twice produces two distinct `Item`s in the tree,
not one overwritten in place — that's the mechanism MVCC snapshots rely on (see
[db.md](db.md#snapshots-and-getat--scanat)).

## Data structure: B-tree, not a skiplist

We use `github.com/google/btree` with degree `config.DEFAULT_BTREE_DEGREE` (= 32).

Ordering comes from the `btree.Item` interface — one method:

```go
func (a Item) Less(b btree.Item) bool {
    return base.InternalCompare(bytes.Compare,
        base.DecodeInternalKey(a.Key), base.DecodeInternalKey(b.(Item).Key)) < 0
}
```

`base.InternalCompare` orders by user key ascending, then by sequence number **descending** — so
among several versions of the same user key, the newest one sorts first. That total order must
agree with the SSTable index's binary search (also `base.InternalCompare` over `bytes.Compare`
on the decoded user key) — if they ever diverge, index lookups silently miss keys.

Why a B-tree rather than the skiplist most LSM engines use: it gives sorted iteration and
O(log n) point lookups with a well-tested off-the-shelf library, and it's cache-friendlier at
degree 32 (each node holds up to 63 items, so a lookup touches few nodes). The classic reason to
prefer a skiplist — lock-free concurrent inserts — doesn't apply here because we serialise
everything behind a mutex anyway.

## Operations

All five methods take the lock: `Put`/`Delete` take the write lock, `Get`/`Size`/`Ascend` take
the read lock. `RWMutex` means concurrent reads don't block each other.

### `Put(internalKey, value)`

```go
newItem := Item{Key: internalKey, Value: value}
tree.ReplaceOrInsert(newItem)
size += len(newItem.Key) + len(newItem.Value)
```

`Put` takes an **already-encoded internal key** — the caller (`db.Put`, via `db.encodeNextKey`)
has already stamped it with a fresh sequence number before calling in. Because every write's key
is distinct (a different trailer each time), `ReplaceOrInsert` never actually overwrites a
previous version of a user key here — each call adds a new B-tree entry. There's no
"un-count the old version" step: every insert is a genuinely new item, so `size` simply
accumulates. One consequence: repeatedly writing the same user key grows the memtable
proportional to the number of writes, not the number of distinct keys (see Known limitations).

### `Get(userKey, snapshot)`

`Get` takes a **user key** plus a `base.SeqNum` snapshot bound (`base.SeqNumMax` sees the latest
write). It builds a search key (`userKey` + `snapshot`, via `base.MakeSearchKeyAt`), which sorts
before every version of that user key visible at or before `snapshot`, then walks forward one
step:

```go
tree.AscendGreaterOrEqual(Item{Key: encodeSearchKey(userKey, snapshot)}, func(i btree.Item) bool {
    item := i.(Item)
    if !bytes.Equal(DecodeInternalKey(item.Key).UserKey, userKey) {
        return false   // walked past this user key entirely — absent
    }
    found = &item
    return false       // first hit at/below snapshot is the newest visible version; stop
})
```

Because internal keys sort by sequence number *descending* within a user key, and the search key
itself carries `snapshot`, the first matching entry the ascend reaches is exactly the newest
version with `seqNum <= snapshot` — no separate visibility filter is needed the way the
range-scan iterator needs one (see below).

The result is **three-valued**, matching the SSTable layer's `KEY_LOOKUP_ENUM` (both now share
`base.KEY_LOOKUP_ENUM`):

| result | meaning | what `db.GetAt` does |
|---|---|---|
| `KEY_ABSENT` | this memtable says nothing about the key at this snapshot | keep searching the SSTables |
| `KEY_FOUND` | newest visible version is a live value | return it |
| `KEY_DELETED` | newest visible version is a tombstone | stop — return not-found |

The distinction is not cosmetic. Collapsing absent and tombstoned into one plain `bool` would
make `db.GetAt` fall through to `searchSSTables` in both cases — and this sequence would return
the deleted value:

```
Put("gone", "back")   →  lives in the memtable
ForceFlush()          →  "back" is now in an SSTable
Delete("gone")        →  tombstone is in the memtable
Get("gone")           →  "back"    ← wrong, the tombstone would be invisible
```

Regression test: `TestGetStopsAtTombstone` (and `TestGetStopsAtTombstoneAfterRestart`) in
`db/operations_test.go`.

### `Delete(internalKey)`

Deletes do not remove anything. `Delete` calls the same `insert` helper as `Put`, with a nil
value — the internal key's trailer already carries `base.InternalKeyKindDelete`, so the tombstone
needs no separate flag:

```go
memTable.insert(internalKey, nil)
```

Why keep it at all: the key may exist in an SSTable on disk. Removing it from the memtable would
just make the old on-disk value visible again. The tombstone is a *newer* record (higher sequence
number) that shadows it, and it must be flushed to disk and propagate through compaction before
the key is truly gone — and even then only once no active snapshot could still need the version
it shadows (see [compaction.md](compaction.md)).

Note that a `Delete` of a key that was never present still inserts a tombstone and still grows
`size`.

### `Size()`

Returns the running byte estimate: `sum(len(key) + len(value))` over every item, where `key` is
the full encoded internal key (user key + 8-byte trailer). It counts **payload bytes only** — no
B-tree node overhead, no per-entry framing (the keyLen/valLen fields the SSTable block format
adds). So real memory use is meaningfully higher than `Size()` reports, and the flushed SSTable is
larger than `Size()` bytes. Because every write is a distinct internal key (see `Put` above),
`Size()` also grows on every write to an existing key, not just on new keys — MVCC means old
versions are live data, by design, until compaction can prove no snapshot still needs them.

`db.flushIfFullLocked` compares this against `DEFAULT_MEMTABLE_FLUSH_SIZE` (4 MB) to decide when
to flush — called from both `db.Put` and `db.Delete` (see [db.md](db.md)).

### `Ascend(fn)`

In-order traversal, the whole reason the tree is sorted:

```go
memTable.Ascend(func(key, value []byte) bool { ... })
```

`key` is the raw encoded internal key (trailer included), so the callback can recover both the
user key and the kind via `base.DecodeInternalKey`. Returning `false` from the callback stops
iteration early.

Two consumers:
- `sstable.Flush` — streams entries out in sorted order to build blocks and the index.
- `db.PrintMemTable` — debug dump, prints `[tombstone]` for deleted keys, with the sequence
  number alongside each user key.

`Ascend` holds the **read** lock for its entire duration. `sstable.Flush` runs inside it, so a
flush blocks all writers until the file is fully written and `fsync`'d.

### `Iterator` (`iterator.go`) — one source in the range-scan merge

```go
func NewIterator(mt *MemTable, lowerBound, upperBound []byte, snapshot base.SeqNum) *Iterator
```

Unlike `sstable.Iterator` (see [sstable.md](sstable.md)), this one is **eager**, not lazy: it
calls `mt.tree.AscendGreaterOrEqual(pivot, ...)` once, up front, and copies every matching `Item`
visible at `snapshot` into a plain `[]Item` slice, stopping the moment it passes `upperBound`.
`Next()` then just walks that slice with a position index. This is safe specifically because the
memtable is already bounded, in-memory data — there's no disk cost to defer the way there is for
an SSTable's blocks, so eagerly snapshotting the range is simpler and just as cheap.

Unlike the point-lookup case in `Get`, the snapshot filter here can't be folded entirely into the
seek key: a range scan crosses *many* user keys, and each one can have its own too-new version
sitting in the tree, so the collecting callback checks `ik.Visible(snapshot)` per entry and skips
(without stopping) any version newer than the snapshot allows.

Exists purely to satisfy `db`'s `source` interface (`Valid`/`Key`/`Value`/`Next`) so `db.Scan`'s
k-way merge can treat the memtable and every SSTable identically — see [db.md](db.md) for the
merge itself. The keys it yields are still full encoded internal keys; the merge logic decodes
kind and user key itself rather than the iterator doing it.

## Lifecycle

```
db.Open      → NewMemTable(), then WAL replay re-applies every entry into it
db.Put       → wal.Put, then memtable.Put, then flushIfFullLocked (Size() >= 4MB?)
db.Delete    → wal.Delete, then memtable.Delete, then the same flushIfFullLocked check
flush        → sstable.Flush(memtable) → memtable = NewMemTable() → WAL truncated
```

The old memtable is simply dropped and garbage-collected. There is no immutable-memtable
handoff and no background flush — flush is synchronous and blocking.

## Known limitations

- **No immutable memtable.** Writes stall for the full duration of a flush + compaction.
- **`Size()` undercounts real memory.** Payload bytes only; the real footprint (B-tree overhead,
  per-entry framing) is larger.
- **Every write, not just every distinct key, occupies memtable space.** MVCC means a key
  overwritten 1000 times before a flush leaves 1000 versions live in the tree until compaction can
  prove none are still needed by an active snapshot — there is no in-memtable analogue of the old
  "replace in place" behavior, by design.

## Related

- [wal.md](wal.md) — durability; what refills the memtable on restart
- [sstable.md](sstable.md) — where `Ascend` output goes
- [db.md](db.md) — flush trigger, read ordering, snapshots, and `Scan`'s use of `Iterator`
- [config.md](config.md) — `DEFAULT_BTREE_DEGREE`, `DEFAULT_MEMTABLE_FLUSH_SIZE`
