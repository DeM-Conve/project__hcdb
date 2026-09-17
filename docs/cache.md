# Cache — shared block cache

Package: `cache/` — files: `lru.go`, `operations.go`, `sharded.go`

## Why it exists

`sstable.readBlock` used to `os.Open`, `Seek`, `io.ReadFull`, and `decodeBlock` on **every**
lookup that got past the bloom filter — even for the same block read a moment earlier. This
package exists to make the second read of a hot block free: skip the disk I/O and the decode
entirely, and hand back the already-decoded `[]BlockEntry` slice.

Modeled on `golang/groupcache/lru` — small, canonical, well-tested — with two intentional
narrowings: keys are `string`, not `any`, and there's no `OnEvicted` callback, since HCDB doesn't
need either yet.

## Type

```go
type entry struct {
    key   string
    value any
}

type LRU struct {
    mu       sync.RWMutex
    capacity int
    ll       *list.List               // front = most recently used
    items    map[string]*list.Element // key -> node in the list, O(1) lookup
}
```

`value` is `any`, not `[]byte` — the whole point is to cache **decoded** blocks
(`[]sstable.BlockEntry`), not raw bytes, so the decode cost is what actually gets skipped on a
hit. Callers type-assert back to the concrete type they stored.

`LRU` now carries its own `sync.RWMutex` — `insert` (called by `Put`) and `Get` both take the
write lock (`Get` still mutates the list via `MoveToFront`, so it can't use `RLock`); `Len` takes
the read lock. This is what makes a single `*cache.LRU` safe to share across the concurrent
readers `sstable.readBlock` can now see (see Known limitations for what changed with this).

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

## Sharding (`sharded.go`)

A single `*LRU`'s mutex serializes every `Get`/`Put` across every SSTable in the database — under
concurrent reads that lock becomes the bottleneck the mutex was added to avoid trading for.
`ShardedLRU` spreads the cache across N independent `*LRU` instances:

```go
type Cacher interface {
    Get(key string) (any, bool)
    Put(key string, value any)
}

type ShardedLRU struct {
    shards []*LRU
}

func shardIndex(key string, numShards int) int {
    h := fnv.New32a()
    h.Write([]byte(key))
    return int(h.Sum32() % uint32(numShards))
}
```

Each `Get`/`Put` hashes the cache key (FNV-1a) to pick one shard and only takes that shard's lock
— unrelated keys in different shards never contend. `Len()` sums every shard's `Len()`. `db.Open`
constructs one `cache.ShardedLRU` with `config.DEFAULT_CACHE_SHARD_COUNT` (16) shards of
`config.DEFAULT_BLOCK_CACHE_ENTRIES / DEFAULT_CACHE_SHARD_COUNT` entries each — so the *total*
entry budget is unchanged from a single unsharded LRU, just distributed. `Cacher` is the interface
`sstable.SSTable.cache` actually holds, satisfied by both `*LRU` and `*ShardedLRU` — plain `*LRU`
is still usable directly (and is what each shard is, underneath).

## Wired into HCDB

One shared `cache.Cacher` per `DB` (a `*ShardedLRU` in practice), not one per SSTable:

```go
// db.Open
blockCache := cache.NewShardedLRU(
    config.DEFAULT_CACHE_SHARD_COUNT,
    config.DEFAULT_BLOCK_CACHE_ENTRIES/config.DEFAULT_CACHE_SHARD_COUNT,
)
for _, sst := range tables {
    sst.SetCache(blockCache)
}
```

Every `sstable.SSTable` holds the same `Cacher` (`sst.SetCache`), so all SSTables compete for the
same fixed budget — a hot block in one file naturally evicts a cold block from another, which is
the correct behavior. `flushMemtable`/`flushMemtableLocked` attaches the same cache to the newly
flushed SSTable and to every SSTable that comes back from `compaction.Compact`.

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

The key is `FilePath + offset`, not a numeric file ID — HCDB doesn't have a numeric file ID
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

- **Plain LRU per shard, not scan-resistant.** A large sequential scan will still evict a shard's
  entire hot working set in one pass. Production engines use a scan-resistant policy (CLOCK-Pro,
  or InnoDB's split young/old LRU) precisely to avoid this; HCDB does not yet.
- **Sharding is unweighted and fixed at 16.** Every shard gets an equal slice of the entry budget
  regardless of actual key distribution, and `DEFAULT_CACHE_SHARD_COUNT` isn't tuned against a
  real contention measurement.
- **No eviction-order test on `ShardedLRU` itself, no hit-rate counter.** Sizing
  (`config.DEFAULT_BLOCK_CACHE_ENTRIES = 256` total) is a starting guess, not tuned against real
  hit rate.
- **Stale entries on file deletion.** If `compaction.Compact` deletes an SSTable file that still
  has cached blocks, those entries sit in the cache holding data for a file that no longer
  exists. Harmless today only because nothing ever looks them up again by that exact
  `FilePath:offset` key once the SSTable is gone — but it's wasted cache budget, and there's no
  explicit `Remove(key)` to reclaim it.

## Related

- [sstable.md](sstable.md) — the only caller, `readBlock`
- [db.md](db.md) — where the shared instance lives and gets attached
- [config.md](config.md) — `DEFAULT_BLOCK_CACHE_ENTRIES`
