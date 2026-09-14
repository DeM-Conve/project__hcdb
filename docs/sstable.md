# SSTable (Sorted String Table)

Package: `sstable/` — files: `sstable_types.go`, `sstable.go`, `block.go`, `index.go`,
`writer.go`, `write_from_entries.go`, `write_block_collector.go`, `reader.go`,
`block_iterator.go`, `sort_entries.go`, `atomic_install.go`, `iterator.go`

## Why it exists

An SSTable is an **immutable, sorted, on-disk file** produced by flushing a memtable. Once
written it is never modified — the only operations are read it, or delete it after compaction
has merged it into a new file.

Immutability is what makes the whole design work: no in-place updates means no locking on
reads, no torn writes, no fragmentation, and safe concurrent access.

## File layout

```
+---------------------------+  offset 0
|  Block 1  (~4KB)          |
|  Block 2                  |
|  ...                      |
+---------------------------+  ← indexOffset
|  IndexEntry 1             |
|  IndexEntry 2             |
|  ...                      |
+---------------------------+  ← bloomOffset
|  bloomLen (4B) + bloom    |
+---------------------------+
|  Footer (20B)             |
+---------------------------+  EOF
```

Data first, metadata last. That ordering is forced by the write path: we're streaming entries
sequentially and we don't know block offsets until we've written the blocks, so the index can
only be written afterwards. The footer at a **fixed offset from EOF** is what makes the file
readable at all — it's the only thing we can locate without scanning.

Files are named `<UnixNano>.sst` in `Config.SSTDir`. The timestamp name is load-bearing: it is
the only recency information the system has (see *Ordering* below).

### Block format (`block.go`)

```
+----------------------+
| numEntries (4B)      |
+----------------------+
| entry 1              |
| entry 2   ...        |
+----------------------+
| CRC32 (4B)           |   ← over numEntries + all entries
+----------------------+
```

### BlockEntry

```
+---------+---------+---------------+--------+
| keyLen  | valLen  | internal key  | value  |
| 4 bytes | 4 bytes | bytes         | bytes  |
+---------+---------+---------------+--------+
```

`key` is an **encoded internal key**: the user key plus an 8-byte trailer packing a sequence
number and a kind (`base.InternalKeyKindSet` / `base.InternalKeyKindDelete`), so `keyLen` is
`len(userKey) + 8`. There is no separate type byte any more — put vs. delete lives entirely in
the trailer, and tombstones are stored on disk exactly like values, just with an empty value and
the delete kind. See `internal/base/internal.go`.

`decodeBlock` verifies the CRC before parsing anything, and returns `"block CRC mismatch"` on
failure. Unlike the WAL, there is no truncate-and-recover — an SSTable is not append-only, so a
corrupt block is a hard read error that propagates up.

### IndexEntry (`index.go`)

```
+---------+-------+---------+---------+
| keyLen  | key   | offset  | length  |
| 4 bytes | bytes | int64   | int32   |
+---------+-------+---------+---------+
```

One entry per block, holding that block's **first internal key** (user key + 8-byte trailer,
so `keyLen` covers the trailer too), its byte offset in the file, and its byte length. So the
index is a *sparse* index — one key per ~4 KB block, not per record. That's the whole point: a
100 MB SSTable needs only ~25 000 index entries, which fits comfortably in RAM, whereas a dense
index would not.

Ordering is `base.InternalCompare`: user key ascending, then sequence number descending — so a
user key with several versions spanning a block boundary still sorts correctly, newest first.

```
Block 1: [a, b, c]        Index:  [a → offset,len]
Block 2: [d, e, f]                [d → offset,len]
Block 3: [g, h, i]                [g → offset,len]
```

### Footer (last 20 bytes)

```
| indexOffset (int64, 8B) | numEntries (uint32, 4B) | bloomOffset (int64, 8B) |
```

Read by `readFooterWithBloom` via `Seek(-20, io.SeekEnd)`.

> `index.go` also still contains `encodeFooter` / `readFooter` — the older 12-byte footer
> without `bloomOffset`. Both are now **dead code**; nothing calls them. They predate the bloom
> filter being added to the format.

## Write path

Two entry points that produce byte-identical output:

| Function | Source | Used by |
|---|---|---|
| `Flush(memTable, dirPath)` (`writer.go`) | live memtable, via `Ascend` | `db.flushMemtable` |
| `WriteSSTableFromBlockEntries(path, entries)` (`write_from_entries.go`) | a `[]BlockEntry` slice | `compaction.mergeGroup` |

Both do the same sequence:

1. Create `<dirPath>/<UnixNano>.sst`, wrap in a `bufio.Writer`.
2. Create a bloom filter sized for `DEFAULT_BLOOM_EXPECTED_KEYS`, and `Add` every key.
3. Accumulate entries into a `blockCollector` until its estimated size reaches
   `DEFAULT_BLOCK_SIZE` (4 KB), then flush that block and record an `IndexEntry`.
4. Flush the trailing partial block.
5. `encodeIndex` — write all index entries. `indexOffset` = the offset where blocks ended.
6. Write `bloomLen` + serialised bloom bytes. `bloomOffset` is computed **arithmetically** as
   `indexOffset + indexSize(indexEntries)`, not observed — because the `bufio.Writer` hides the
   real file position. `indexSize` re-derives the encoded size as `4 + len(key) + 8 + 4` per
   entry. **This must stay in sync with `encodeIndex` byte-for-byte** or every read breaks.
7. `encodeFooterWithBloom`, `writer.Flush()`, `file.Sync()` — the fsync is what makes the
   SSTable durable, which is what allows the WAL to be truncated afterwards.
8. `atomicInstall(tmpPath, finalPath)` (`atomic_install.go`) — both writers actually write to
   `<finalPath>.tmp` first (not directly to `<finalPath>`), then this renames it into place. See
   *Atomic install* below.

### `blockCollector` (`write_block_collector.go`)

A small accumulator:

```go
type blockCollector struct {
    entries      []BlockEntry
    sizeEstimate int
    lastFirstKey []byte    // first key of the block currently being built
}
```

`add` bumps `sizeEstimate` by `len(key) + len(value) + 8` — the 8 being the per-entry framing
(`keyLen 4 + valLen 4`; `key` is already the full encoded internal key, trailer included, so no
separate accounting is needed for it). It's an estimate, not exact: it ignores the block's own 4-byte
`numEntries` header and 4-byte CRC, so real blocks run ~8 bytes over. Also, the size check
happens *after* adding, so a block can overshoot 4 KB by one entry's worth. Neither matters —
4 KB is a target, not a constraint.

`lastFirstKey` is captured when the collector is empty (i.e. on the first entry of a new block)
and is what goes into the `IndexEntry`. `drain` returns the entries and resets all three fields.

> Naming note: `lastFirstKey` means "first key of the block being collected", and it must be
> read *before* `drain()` clears it. Both writers do this correctly.

### Atomic install (`atomic_install.go`)

```go
func atomicInstall(tmpPath, finalPath string) error {
    os.Rename(tmpPath, finalPath)                    // atomic at the filesystem level
    dir, _ := os.Open(filepath.Dir(finalPath))
    defer dir.Close()
    return dir.Sync()                                 // fsync the directory entry itself
}
```

Before this existed, both writers wrote directly to `<UnixNano>.sst` — a crash mid-write left a
corrupt, partially-written file at the path a reader would trust. Now they write to
`<finalPath>.tmp`, `fsync` the file's contents, then rename. `os.Rename` is atomic, so there is
no observable state where `finalPath` exists but is incomplete: either the `.tmp` file is still
there (crash before rename) or the complete file is at `finalPath` (crash after). Both `Flush`
and `WriteSSTableFromBlockEntries` also `defer` an `os.Remove(tmpPath)` on any error return, so a
failed write doesn't leave an orphaned `.tmp` file behind.

The directory `fsync` covers a subtler gap: `rename()` returning success only means the new
directory entry hit the kernel's page cache, not disk — a crash before *that* is flushed can
still lose the rename despite the syscall having already returned successfully. See
[faultinjection.md](faultinjection.md) for the crash-window test this enabled
(`db.TestCrashBetweenFlushAndWALReset`).

### Ordering requirement

Entries must arrive already sorted. `Flush` gets that for free from `memtable.Ascend`.
`WriteSSTableFromBlockEntries` relies on the caller — `compaction.mergeIterators` calls
`SortEntries` (`sort_entries.go`, a `sort.Slice` on `string(Key)`) before writing. If unsorted
entries were written, the index's binary search would silently return wrong results.

## Read path

### `Open(filepath)` (`reader.go`)

1. Read the 20-byte footer → `indexOffset`, `numEntries`, `bloomOffset`.
2. `decodeIndex` — seek to `indexOffset`, read `numEntries` index entries into memory.
3. `readBloom` — seek to `bloomOffset`, read `bloomLen` then that many bytes, `Deserialize`.
4. Return `&SSTable{FilePath, index, bloom}` and **close the file**.

Index and bloom are held in memory for the lifetime of the `SSTable`; the data blocks are not.
The file handle is closed and reopened per block read (see limitations).

### `OpenAllInDir(dirPath)`

Reads the directory, sorts entries by name **descending** (`Name()[i] > Name()[j]`), and opens
each one. Since names are `UnixNano` timestamps, descending name order = **newest first**. That
slice order *is* the recency ordering the whole read path depends on.

### `Lookup(userKey, snapshot)` (`sstable.go`)

```
userKey
 └─ bloom.MightContain(userKey)?          (bloom indexes user keys, not internal keys)
      ├─ no  → KEY_ABSENT                  (zero disk I/O — the fast path)
      └─ yes → NewIterator(sst, userKey, snapshot) → seek to newest version visible at snapshot
                 ├─ not valid, or landed on a different user key → KEY_ABSENT
                 └─ found → KEY_DELETED (tombstone) or KEY_FOUND (value)
```

Returns a three-valued `base.KEY_LOOKUP_ENUM`, shared with the memtable:

```go
KEY_ABSENT   // not in this table (at this snapshot) — caller should keep searching older tables
KEY_FOUND    // found, value returned
KEY_DELETED  // tombstone — the key IS deleted, caller must STOP searching
```

Distinguishing `KEY_DELETED` from `KEY_ABSENT` is essential to correctness. Collapsing them
would make a deleted key resurrect from an older SSTable.

`Lookup` no longer does its own `searchIndex` call directly — it delegates the seek to
`Iterator` (see below). The reason: a **search key** for `userKey` sorts *before* every real
version of that user key, so when `userKey` happens to be exactly a block's recorded first key,
`searchIndex` alone would point at the *previous* block. `Iterator` walks forward across block
boundaries and lands on the correct entry regardless, which also handles a user key whose
versions straddle two blocks.

### `searchIndex` (`index.go`)

Binary search over **internal keys** for the rightmost index entry whose `FirstKey` compares
`<=` the target (`base.InternalCompare`, user key ascending then sequence number descending):

```go
if base.InternalCompare(bytes.Compare, firstKey, target) <= 0 {
    result = mid; lo = mid + 1     // candidate; try further right
} else {
    hi = mid - 1
}
```

Index `[a, d, g]`, search for an internal key on user key `"e"` → returns block 2 (`d`), because
`e` sorts between `d` and `g` so it can only live in the block starting at `d`. Returns `-1` when
the target is smaller than every first key — it cannot exist in the file at all.

`searchIndex` alone only picks a **candidate starting block** — it is not the final word on
whether the key is in that block, because a *search key* (used to seek to "the newest version of
this user key") sorts before every real version of that key, including one that happens to be a
block's own first key. `Iterator` (below) is what actually walks forward from that candidate
block and lands on the right entry; `Lookup` no longer does a standalone single-block scan.

### `BlockIterator` (`block_iterator.go`)

Reads **every** block of an SSTable, decodes them all, and returns one flat `[]BlockEntry` in
key order. Used only by compaction. It's a full materialisation, not a streaming iterator —
despite the name, it loads the entire table into memory at once. That's the dominant memory
cost of compaction.

### `Iterator` (`iterator.go`) — the lazy cursor behind both `Lookup` and range scans

```go
func NewIterator(sst *SSTable, lowerBound []byte, snapshot base.SeqNum) (*Iterator, error)
```

`NewIterator` builds a search key for `lowerBound` at `snapshot` (`EncodeSearchKeyAt`), uses
`searchIndex` to pick a starting block, loads it (through `readBlock`, so it transparently
benefits from the block cache — see [cache.md](cache.md)), then walks forward entry-by-entry —
crossing into the next block via `advance()` once the current one is exhausted — until it reaches
an entry at or after the seek key. A second pass, `skipInvisible`, then skips forward past any
entry whose sequence number is newer than `snapshot`; this has to be a separate, ongoing check
(not just applied once at the seek point) because walking forward crosses into other user keys
whose *own* newest version may itself be too new.

Two call sites, both benefiting from the same laziness — never more than one decoded block held
in memory at a time, which matters because an SSTable can be far larger than RAM:

- **`Lookup`** constructs one `Iterator` at the target user key and reads at most the position it
  lands on — effectively still "one block read per lookup" as before, just implemented via the
  general seek-and-filter machinery instead of a bespoke single-block linear scan.
- **`db.Scan`**'s k-way merge (see [db.md](db.md)) holds one long-lived `Iterator` per SSTable and
  calls `Next()` repeatedly, which is `advance()` plus `skipInvisible()`.

`BlockIterator` and `Iterator` exist for different reasons and aren't interchangeable:
`BlockIterator` eagerly loads a whole table for compaction's merge step; `Iterator` never holds
more than one block, which is what range scans and point lookups both need.

## Known limitations

- **A file handle is opened and closed per block read.** `readBlock` calls `os.Open` on every
  lookup that misses the block cache. No handle cache, no mmap. The block cache
  ([cache.md](cache.md)) now absorbs most of this cost for repeated access to the same block, but
  a cold miss still pays it.
- **`BlockIterator` loads whole tables into RAM**, so compaction memory scales with the size of
  the group being merged. `sstable.Iterator` (used by both `Lookup` and range scans) does not
  have this problem.
- **No reference counting protects a file an `Iterator` is still reading.** If `compaction.Compact`
  deletes the underlying SSTable file while a `db.Scan` iterator is mid-scan over it, that
  iterator's next `readBlock` call fails — confirmed still true: neither `sstable.Iterator` nor
  `compaction.compact.go`'s `os.Remove` step have any notion of an open reader to wait for (see
  [db.md](db.md)).
- **`indexSize` duplicates `encodeIndex`'s layout knowledge.** Any change to the index encoding
  must be mirrored in both or every file becomes unreadable.
- **`encodeFooter` / `readFooter` are dead code** (the pre-bloom 12-byte footer).
- **No compression, no prefix compression, no restart points** inside blocks.

## Related

- [bloomfilter.md](bloomfilter.md) — the pre-read filter
- [compaction.md](compaction.md) — how SSTables get merged and deleted
- [memtable.md](memtable.md) — the source of a flushed table
- [cache.md](cache.md) — what `readBlock` checks before hitting disk
- [faultinjection.md](faultinjection.md) — why atomic install exists, and what tests it
- [db.md](db.md) — `Scan`'s k-way merge, which drives `sstable.Iterator`
- [config.md](config.md) — `DEFAULT_BLOCK_SIZE`, `DEFAULT_BLOOM_EXPECTED_KEYS`,
  `DEFAULT_BLOCK_CACHE_ENTRIES`
