# Cache — shared block cache

Package: `cache/` — files: `lru.go`, `operations.go`

## Why it exists

`sstable.readBlock` used to `os.Open`, `Seek`, `io.ReadFull`, and `decodeBlock` on **every**
lookup that got past the bloom filter — even for the same block read a moment earlier. This
package exists to make the second read of a hot block free: skip the disk I/O and the decode
entirely, and hand back the already-decoded `[]BlockEntry` slice.

Modeled on `golang/groupcache/lru` — small, canonical, well-tested — with two intentional
narrowings: keys are `string`, not `any`, and there's no `OnEvicted` callback, since hcdb doesn't
need either yet.

## Type

```go
type entry struct {
    key   string
    value any
}

type LRU struct {
    capacity int
    ll       *list.List               // front = most recently used
    items    map[string]*list.Element // key -> node in the list, O(1) lookup
}
```

`value` is `any`, not `[]byte` — the whole point is to cache **decoded** blocks
(`[]sstable.BlockEntry`), not raw bytes, so the decode cost is what actually gets skipped on a
hit. Callers type-assert back to the concrete type they stored.

## Design: hashmap + doubly-linked list

The classic O(1) LRU trick: the map doesn't store the value, it stores a pointer to the value's
**node inside the linked list**. That's what makes both "find by key" (map lookup) and "mark
most-recently-used" (splice to front) O(1) at once — `container/list` is a real doubly-linked
list, so moving a node is pointer relinking, not copying.

- `Get(key)` — map lookup, then `ll.MoveToFront(elem)`.
- `Put(key, value)` → `insert(key, value)` (`lru.go`) — update-and-move-to-front if the key
  exists, else `PushFront` a new node; then evict if over capacity.
- `evictOldest` — `ll.Back()` is always the least-recently-used, by construction, since every
  `Get`/`Put` moves its node to the front. Removes it from both the list and the map.

`capacity == 0` means **unbounded** — matches `groupcache`'s `MaxEntries` semantics. Without this
check, a zero-value cache would evict every item immediately after inserting it.

`insert`/`evictOldest` live in `lru.go` (the list/map mechanics); `Get`/`Put`/`Len` — the public
API — live in `operations.go`, matching the rest of the codebase's types-vs-behavior split.

## Wired into hcdb

One shared `*cache.LRU` per `DB`, not one per SSTable:

```go
// db.Open
blockCache := cache.NewLRU(config.DEFAULT_BLOCK_CACHE_ENTRIES)
for _, sst := range tables {
    sst.SetCache(blockCache)
}
```

Every `sstable.SSTable` holds a **pointer** to the same instance (`sst.SetCache`), so all
SSTables compete for the same fixed budget — a hot block in one file naturally evicts a cold
block from another, which is the correct behavior. `flushMemtable` attaches the same cache to
the newly flushed SSTable and to every SSTable that comes back from `compaction.Compact`.

`sstable.readBlock` (`sstable.go`) is the only call site:

```go
cacheKey := fmt.Sprintf("%s:%d", sst.FilePath, idx.Offset)   // (fileID, blockOffset)

if sst.cache != nil {
    if cached, ok := sst.cache.Get(cacheKey); ok {
        return cached.([]BlockEntry), nil
    }
}
// ... disk read + decodeBlock on miss ...
if sst.cache != nil {
    sst.cache.Put(cacheKey, entries)
}
```

The key is `FilePath + offset`, not a numeric file ID — hcdb doesn't have a numeric file ID
system, and the file path is already unique per SSTable.

## Measured impact

`BenchmarkGetSSTableHit`, repeated `Get` calls hitting the same block, before vs after wiring:

| Metric | Before | After | Change |
|---|---|---|---|
| ns/op | ~21,000 | ~560 | ~37× faster |
| MB/s | 0.24 | ~8.9 | ~37× throughput |
| B/op | ~19,700 | 232 | ~85× fewer bytes |
| allocs/op | 796 | 4 | ~200× fewer allocations |

The 796 → 4 allocs/op is the real story: `decodeBlock` allocates a fresh `[]BlockEntry` plus a
`[]byte` pair per entry on every miss. A cache hit skips all of that — the residual 4 allocs/op
is just `LRU.Get`'s own bookkeeping (map lookup + `MoveToFront`).

## Known limitations

- **Not concurrency-safe.** No mutex anywhere in this package — a single-threaded LRU only,
  exactly matching Feature 6's incremental scope. Concurrent `Get`/`Put` from multiple goroutines
  (which `sstable.readBlock` can now trigger, since `db.Get`/`Scan` only take `db.mu.RLock`) race
  on the map and the list.
- **Plain LRU, not scan-resistant.** A large sequential scan will evict the entire hot working
  set in one pass. Production engines use a scan-resistant policy (CLOCK-Pro, or InnoDB's
  split young/old LRU) precisely to avoid this; hcdb does not yet.
- **No eviction-order test, no hit-rate counter, no sharding.** Sizing
  (`config.DEFAULT_BLOCK_CACHE_ENTRIES = 256`) is a starting guess, not tuned against real hit
  rate.
- **Stale entries on file deletion.** If `compaction.Compact` deletes an SSTable file that still
  has cached blocks, those entries sit in the cache holding data for a file that no longer
  exists. Harmless today only because nothing ever looks them up again by that exact
  `FilePath:offset` key once the SSTable is gone — but it's wasted cache budget, and there's no
  explicit `Remove(key)` to reclaim it.

## Related

- [sstable.md](sstable.md) — the only caller, `readBlock`
- [db.md](db.md) — where the shared instance lives and gets attached
- [config.md](config.md) — `DEFAULT_BLOCK_CACHE_ENTRIES`
