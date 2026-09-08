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
+--------+---------+---------+-------+--------+
| Type   | keyLen  | valLen  | key   | value  |
| 1 byte | 4 bytes | 4 bytes | bytes | bytes  |
+--------+---------+---------+-------+--------+
```

`Type` is `OP_PUT` or `OP_DELETE` — tombstones are stored on disk exactly like values, just
with the delete flag and an empty value.

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

One entry per block, holding that block's **first key**, its byte offset in the file, and its
byte length. So the index is a *sparse* index — one key per ~4 KB block, not per record. That's
the whole point: a 100 MB SSTable needs only ~25 000 index entries, which fits comfortably in
RAM, whereas a dense index would not.

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

`add` bumps `sizeEstimate` by `len(key) + len(value) + 9` — the 9 being the per-entry framing
(`type 1 + keyLen 4 + valLen 4`). It's an estimate, not exact: it ignores the block's own 4-byte
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

### `Lookup(key)` (`sstable.go`)

```
key
 └─ bloom.MightContain(key)?
      ├─ no  → KEY_ABSENT              (zero disk I/O — the fast path)
      └─ yes → searchIndex(index, key) → blockIdx
                 ├─ blockIdx < 0 → KEY_ABSENT   (key sorts before every block's first key)
                 └─ readBlock(offset, length) → decodeBlock → findInBlockLookup
```

Returns a three-valued `KEY_LOOKUP_ENUM`:

```go
KEY_ABSENT   // not in this table — caller should keep searching older tables
KEY_FOUND    // found, value returned
KEY_DELETED  // tombstone — the key IS deleted, caller must STOP searching
```

Distinguishing `KEY_DELETED` from `KEY_ABSENT` is essential to correctness. Collapsing them
would make a deleted key resurrect from an older SSTable.

`Get` is a thin `bool`-returning wrapper over `Lookup` used for simpler call sites.

### `searchIndex` (`index.go`)

Binary search for the **rightmost** index entry with `FirstKey <= key`:

```go
if bytes.Compare(index[mid].FirstKey, key) <= 0 {
    result = mid; lo = mid + 1     // candidate; try further right
} else {
    hi = mid - 1
}
```

Index `[a, d, g]`, search `"e"` → returns block 2 (`d`), because `e` sorts between `d` and `g`
so it can only live in the block starting at `d`. Returns `-1` when `key` is smaller than every
first key — the key cannot exist in the file at all.

**Exactly one block is read.** Because the file is globally sorted, if the key isn't in the
block that `searchIndex` picked, it isn't in the file. `findInBlockLookup` then does a *linear*
scan of that block's entries — fine, since a 4 KB block holds only tens to hundreds of records
and it's already all in memory.

### `BlockIterator` (`block_iterator.go`)

Reads **every** block of an SSTable, decodes them all, and returns one flat `[]BlockEntry` in
key order. Used only by compaction. It's a full materialisation, not a streaming iterator —
despite the name, it loads the entire table into memory at once. That's the dominant memory
cost of compaction.

### `Iterator` (`iterator.go`) — one source in the range-scan merge

Unlike `BlockIterator` above (which materializes a whole table for compaction), `Iterator` is
the **lazy**, streaming per-SSTable cursor used by `db.Scan`'s k-way merge
(see [db.md](db.md)). `NewIterator(sst, lowerBound)` binary-searches `sst.index` the same way
`Lookup` does to find the starting block, then walks forward — decoding one block at a time via
`readBlock` (which transparently benefits from the block cache, see [cache.md](cache.md)) — and
crossing into the next block only once the current one is exhausted. It never loads more than
one decoded block into memory at a time, which is the point: an SSTable can be far larger than
RAM, so a range scan can't afford `BlockIterator`'s eager whole-table load.

## Known limitations

- **A file handle is opened and closed per block read.** `readBlock` calls `os.Open` on every
  lookup that misses the block cache. No handle cache, no mmap. The block cache
  ([cache.md](cache.md)) now absorbs most of this cost for repeated access to the same block, but
  a cold miss still pays it.
- **`BlockIterator` loads whole tables into RAM**, so compaction memory scales with the size of
  the group being merged. `sstable.Iterator` (used by range scans) does not have this problem —
  the two exist for different reasons and aren't interchangeable.
- **The block cache is unbounded across concurrent scans.** `Iterator` doesn't pin blocks it has
  read — if a compaction deletes the underlying file mid-scan, a subsequent `readBlock` call on
  that iterator will fail. No reference counting exists yet (see [db.md](db.md)).
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
