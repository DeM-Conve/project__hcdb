# hcdb — A Re-Onboarding Tutorial (Go + LSM engine, from scratch)

You wrote this 4 months ago and lost touch with Go. This document re-teaches you
**both** at once: every Go concept is taught using a real line from *your* code,
and by the end you'll understand the whole engine end to end.

Read it top to bottom with the code open beside you. It's long on purpose — it's a
refresher, not a summary.

---

## Table of Contents

1. [How to run it right now](#1-how-to-run-it-right-now)
2. [The 10-minute mental model of the whole engine](#2-the-mental-model)
3. [Go language refresher, taught through your code](#3-go-refresher)
4. [Package-by-package walkthrough (read in this order)](#4-package-walkthrough)
5. [Four end-to-end traces](#5-end-to-end-traces)
6. [Go idioms & gotchas that live in this codebase](#6-idioms-and-gotchas)
7. [Exercises to get sharp again](#7-exercises)

---

<a name="1-how-to-run-it-right-now"></a>
## 1. How to run it right now

```bash
cd /home/harsh-chauhan/z_drive/PERSONAL/project__hcdb
go run main.go                 # runs the demo in main.go
go build ./...                 # compile everything, no binary run
go vet ./...                   # static checks
go test ./... -race            # tests + data-race detector
go test ./benchmark/ -bench=. -benchtime=5s -benchmem
```

`main.go` writes some keys, flushes four SSTables (the 4th triggers compaction),
then reads back to prove that latest-write-wins and deletes stick. Run it and watch
`assets/` fill with `*.sst` files.

**A module** is the unit Go builds. Your `go.mod` declares:

```
module github.com/hchauhan7816/hcdb
go 1.24.2
require github.com/google/btree v1.1.3
```

That `module` line is the **import prefix** for every internal package. That's why
you see `import "github.com/hchauhan7816/hcdb/memtable"` everywhere — it's not a
URL fetch, it's *this repo*. Only `google/btree` is a real external dependency
(pinned in `go.sum` by checksum).

---

<a name="2-the-mental-model"></a>
## 2. The mental model (read this twice)

hcdb is an **LSM-tree** (Log-Structured Merge tree) — the same family as LevelDB
and RocksDB. The core trick: **turn random writes into sequential writes.**

### Write path
```
Put("harsh","chauhan")
   │
   1. append to WAL  (disk, append-only, crash-safe)
   │
   2. insert into Memtable  (RAM, a sorted B-tree)
   │
   3. if memtable big enough → Flush to an SSTable (immutable file on disk)
   │
   4. if too many SSTables → Compact (merge them)
```

### Read path
```
Get("harsh")
   │
   1. look in Memtable         → found? return (newest data lives here)
   │
   2. else scan SSTables newest→oldest
        │
        for each SSTable:
          Bloom filter says "definitely not here"? → skip the file entirely
          else: binary-search the sparse index → read ONE ~4KB block → scan it
```

### The four data structures, one sentence each
| Thing | Where | Role |
|---|---|---|
| **WAL** | disk, append-only | durability: replay it after a crash to rebuild the memtable |
| **Memtable** | RAM, B-tree | fast sorted writes; absorbs all mutations until it's full |
| **SSTable** | disk, immutable | sorted key-value file with an index + bloom filter |
| **Bloom filter** | RAM (per SSTable) | "is this key *possibly* in this file?" — avoids useless disk reads |

### Why sorted everywhere?
Because sorted data lets you **binary search on disk** and **merge files by walking
them in lockstep**. The memtable is a B-tree specifically so that when you flush it,
the keys already come out in order.

### Deletes = "tombstones"
You never erase a key. `Delete("b")` writes a special record of type
`OP_DELETE` (a *tombstone*). On read, hitting a tombstone means "return not found."
Tombstones flow through the whole system so a delete in a new file correctly hides
an old value in an older file.

Keep this diagram in your head. Every file below is one box in it.

---

<a name="3-go-refresher"></a>
## 3. Go language refresher, taught through your code

If a concept already feels obvious, skim. Each item cites a real location.

### 3.1 Packages & files
A **package** is a directory. Every `.go` file in `memtable/` starts with
`package memtable`. Files in the same package share everything — no imports needed
between `memtable.go` and `memtable_types.go`. You split by *file for humans*, but
Go sees one package.

**Exported vs unexported = capitalization.** This is the whole access-control system:
- `func (memTable *MemTable) Put(...)` — capital `P` → callable from other packages.
- `size int` in a struct — lowercase → private to package `memtable`.

Look at `memtable/memtable_types.go`:
```go
type MemTable struct {
    mut  sync.RWMutex   // lowercase: private
    tree *btree.BTree   // lowercase: private
    size int            // lowercase: private
}
```
Nobody outside `memtable` can touch `size` directly — they must call the exported
`Size()` method. That's encapsulation, enforced by the compiler via capital letters.

### 3.2 Structs and methods
A **struct** is a bundle of fields (`db/db_types.go`):
```go
type DB struct {
    mu       sync.RWMutex
    wal      *wal.WAL
    memtable *memtable.MemTable
    sstables []*sstable.SSTable
    conf     config.Config
}
```

A **method** is a function with a *receiver* — the `(db *DB)` part
(`db/db_operations.go:16`):
```go
func (db *DB) Put(key, value string) error {
```
Read it as: "Put is a method on `*DB`; inside, `db` refers to the instance." It's
just syntactic sugar for `Put(db *DB, key, value string)`.

### 3.3 Pointers: `*T` and `&`
- `*DB` means "pointer to a DB" — an address, not a copy.
- `&thing` takes the address of `thing`.
- `*p` dereferences (rarely needed by hand; Go auto-dereferences for field/method
  access).

Why pointers matter here: methods that **mutate** must use a pointer receiver,
otherwise they'd modify a *copy* and the change would vanish. `Put` changes the
memtable, so it's `(db *DB)`, and `db.Open` returns `*DB` (`db/db.go:34`):
```go
return &DB{wal: walObj, memtable: mem, sstables: tables, conf: conf}, nil
```
`&DB{...}` = "build a DB, give me its address." Everyone shares that one DB.

**Contrast:** `func (a Item) Less(b btree.Item) bool` in `memtable/memtable.go:8`
uses a *value* receiver `(a Item)` — it only reads, never mutates, and Items are
tiny, so copying is fine.

### 3.4 Slices: Go's dynamic arrays
`[]byte` is a slice of bytes; `[]*sstable.SSTable` is a slice of pointers. A slice
is a view (pointer + length + capacity) over a backing array. `append` grows it:

`db/db.go:86`:
```go
db.sstables = append([]*sstable.SSTable{sst}, db.sstables...)
```
Read this carefully — it's a common Go idiom:
- `[]*sstable.SSTable{sst}` — a new one-element slice holding the fresh table.
- `db.sstables...` — the `...` **spreads** the existing slice as arguments.
- Net effect: **prepend** `sst` to the front. New table becomes index 0 = "newest."

That ordering (newest at front) is load-bearing: reads and merges both rely on
"index 0 wins."

`[]byte(key)` and `string(a.Key)` are **conversions** between strings and byte
slices. Strings are immutable; `[]byte` is mutable. The DB stores bytes on disk but
the public API takes `string` for convenience (`Get(key string)`).

### 3.5 Interfaces: behavior, not data
An **interface** is a set of method signatures. Any type with those methods
*satisfies* it automatically — no `implements` keyword.

Your memtable uses `google/btree`, whose tree stores anything satisfying
`btree.Item`, which requires one method: `Less(than Item) bool`. You provide it
(`memtable/memtable.go:8`):
```go
func (a Item) Less(b btree.Item) bool {
    return string(a.Key) < string(b.(Item).Key)
}
```
Because your `Item` has a `Less` method, it *is* a `btree.Item`. That's Go's
"structural typing." The `b.(Item)` is a **type assertion**: `b` arrives as the
interface type `btree.Item`, and `.(Item)` says "I know it's really an `Item`,
give me that concrete value." You do it again in `Ascend` (`memtable.go:81`):
`item := i.(Item)`.

`io.Reader`, `io.Writer`, `io.ReadSeeker` are the most common std interfaces and
you use them heavily in sstable/wal (e.g. `func encodeIndex(w io.Writer, ...)`).
Anything you can write bytes to — a file, a `bytes.Buffer`, a `bufio.Writer` —
satisfies `io.Writer`, so your encoders work on all of them unchanged. That's the
payoff of coding to interfaces.

### 3.6 Errors: values you return and check
Go has no exceptions. Functions return an `error` as the **last** return value; you
check it immediately. The pattern is everywhere (`db/db.go:19`):
```go
walObj, err := wal.Open(conf.WALPath)
if err != nil {
    return nil, err
}
```
- `:=` declares and assigns in one step (short variable declaration).
- `err != nil` means something went wrong.
- You return `nil` for the useful value and the `err` upward. Callers repeat this.

`fmt.Errorf("...%w", err)` (in `compaction/compact.go:43`) **wraps** an error,
adding context while keeping the original for later inspection. The `%w` verb is
special — it preserves the wrapped error.

Multiple return values are normal: `func (db *DB) Get(key string) ([]byte, bool)` —
returns the value **and** a "found" boolean (the "comma-ok" idiom, section 3.9).

### 3.7 `defer`: run this when the function exits
`defer` schedules a call for when the surrounding function returns — used for
cleanup so you can't forget it (`sstable/sstable.go:42`):
```go
file, err := os.Open(sst.FilePath)
if err != nil { return nil, err }
defer file.Close()   // guaranteed to run on every return path below
```
Deferred calls run in **LIFO** order (last deferred runs first). You also see
`defer db.mu.Unlock()` right after `Lock()` — the canonical way to guarantee an
unlock even if the function returns early or panics.

### 3.8 Concurrency: goroutines & mutexes
This engine doesn't spawn goroutines itself, but it's built to be **safe if
multiple goroutines call it at once**. That safety comes from mutexes.

`sync.RWMutex` = a read/write lock:
- `RLock()`/`RUnlock()` — many readers at once (shared).
- `Lock()`/`Unlock()` — one writer, exclusive.

`db/db_operations.go:6`:
```go
func (db *DB) Get(key string) ([]byte, bool) {
    db.mu.RLock()
    defer db.mu.RUnlock()
    ...
}
```
Reads take the *read* lock (many concurrent Gets are fine). `flushMemtable` takes
the full `Lock()` because it swaps the memtable out — that must be exclusive. The
`defer ...Unlock()` pattern makes it impossible to leak a held lock.

> Sharp-eyes note: `Put`/`Delete` in `db_operations.go` don't take `db.mu` before
> touching `db.memtable`, but the memtable has its *own* internal `mut`, so single
> operations stay safe. Mixing that with a concurrent flush is exactly the kind of
> subtlety `go test -race` exists to catch — a good thing to revisit later.

### 3.9 The "comma-ok" idiom
Two-value returns that mean "value + did-it-exist":
```go
if val, ok := db.memtable.Get([]byte(key)); ok {
    return val, true
}
```
Same shape for map lookups: `if seen[key] { ... }` and `oldItem := old.(Item)`
guarded by `if old != nil`. Learn to read `x, ok := ...; if ok` on sight.

### 3.10 `iota`, constants, typed enums
`config/config.go` uses a `const` block. `sstable/sstable_types.go` builds an enum:
```go
type KEY_LOOKUP_ENUM uint8
const (
    KEY_ABSENT KEY_LOOKUP_ENUM = iota  // 0
    KEY_FOUND                          // 1
    KEY_DELETED                        // 2
)
```
`iota` auto-increments within a `const` block (0,1,2…). Giving it a named type
(`KEY_LOOKUP_ENUM`) means the compiler stops you passing a random int where a
lookup-status is expected. That's a lightweight enum.

### 3.11 Closures (functions that capture variables)
`sstable/write_from_entries.go:32` defines `flushCurrent` *inside* another function:
```go
flushCurrent := func() error {
    ...
    currentOffset = newOffset   // reaches OUTWARD and mutates the enclosing var
}
```
That inner function **captures** `collector`, `currentOffset`, `indexEntries`,
`writer` from its parent scope. Calling `flushCurrent()` reads and writes those
outer variables. Closures are how Go does small local helpers without threading a
dozen parameters around. `Ascend(func(key, value []byte, itemType uint8) bool {...})`
passes a closure as a callback.

### 3.12 `binary.Read`/`binary.Write` — the serialization workhorse
On-disk formats are just bytes. `encoding/binary` reads/writes fixed-width integers
in a chosen byte order. You use **little-endian** everywhere:
```go
binary.Write(buf, binary.LittleEndian, uint32(len(entry.Key)))
```
This writes exactly 4 bytes. On read you mirror it: `binary.Read(r, binary.LittleEndian, &keyLength)`
fills a `uint32` from 4 bytes. The `&keyLength` passes the *address* so the function
can store into your variable. **Every format is symmetric: write order == read
order.** That symmetry is the single most important thing to keep straight in this
codebase.

---

<a name="4-package-walkthrough"></a>
## 4. Package-by-package walkthrough

Read the packages in **this order** — each builds on the previous.

### 4.1 `config/` — the knobs
`config/config.go` is pure constants + a tiny struct. Worth memorizing a few:

| Constant | Value | Meaning |
|---|---|---|
| `OP_PUT` / `OP_DELETE` | 0 / 1 | record type byte used *everywhere* on disk |
| `MAX_KEY_LENGTH` / `MAX_VALUE_LENGTH` | 1KB / 1MB | sanity bounds; corruption guard on replay |
| `DEFAULT_MEMTABLE_FLUSH_SIZE` | 4MB | flush the memtable when it hits this |
| `DEFAULT_BLOCK_SIZE` | 4KB | target block size (≈ OS page) |
| `DEFAULT_SYNC_THRESHOLD` | 10 | fsync the WAL every 10 writes |
| `DEFAULT_COMPACTION_THRESHOLD` | 4 | compact once ≥4 SSTables exist |
| `DEFAULT_SIMILAR_SIZE_RATIO` | 2 | two files are "same tier" if size ratio ≤ 2 |
| `DEFAULT_BLOOM_*` | 1%, 100k | bloom target false-positive rate & expected keys |

`OP_PUT = uint8(0)` and `OP_DELETE = uint8(1)` are the *type* byte that appears in
WAL entries, memtable items, and SSTable block entries. One vocabulary, three layers.

### 4.2 `wal/` — Write-Ahead Log (durability)
The WAL is an **append-only file**. Before any write touches RAM, it's recorded
here, so a crash can be replayed. This is the "ordering is load-bearing" rule: WAL
first, memtable second.

**Record format** (comment at `wal/wal_types.go:8`):
```
[totalLen(4)] [type(1)] [keyLen(4)] [valLen(4)] [key] [value] [crc32(4)]
```

- `wal/open.go` — `os.OpenFile(path, O_CREATE|O_RDWR|O_APPEND, 0644)`. The flags are
  OR'd bit-flags: create if missing, read+write, always append. Wraps the file in a
  `bufio.Writer` (buffers small writes in memory, flushes in bigger chunks — far
  fewer syscalls).
- `wal/append.go` — `Append` builds the data bytes (`writeDataBuff`), computes
  `crc32.ChecksumIEEE` over them, then writes `totalLength`, the data, and the CRC.
  Study `writeDataBuff`: it's the canonical "serialize a record" routine — write the
  type, then the two lengths, then the raw key and value bytes.
- `wal/operators.go` — `Put`/`Delete` are the public API. They build an `Entry`,
  call `Append`, bump `putCounter`, and every `DEFAULT_SYNC_THRESHOLD` (10) ops call
  `Sync()`. **This is the durability/throughput trade-off**: between syncs, up to 9
  acknowledged writes could be lost on a hard power cut. Deliberate.
- `wal/sync.go` — `Sync()` = flush the buffer to the OS, then `File.Sync()` = force
  the OS to flush to the physical disk. Two different flushes; you need both.
- `wal/replay.go` — the crash-recovery reader, and the most instructive file in the
  package. It seeks to offset 0 and loops reading records. **Key robustness idea:**
  on *any* corruption (short read, bad length, CRC mismatch) it `Truncate`s the file
  back to `startOffset` and stops. This means a half-written record from a crash is
  cleanly chopped off and the DB opens with the last good prefix — "treat a corrupt
  WAL tail as truncatable, not fatal." The `countingReader` is a tiny wrapper that
  tracks the byte offset as it reads, so it knows where the current record *started*.

  Notice the layered validation before trusting anything:
  1. `totalLength` sane? (≤ max possible record)
  2. full record bytes actually present? (`io.ReadFull`)
  3. stored CRC == recomputed CRC?
  4. keyLen/valLen within bounds?

  Only after all four does it append the `Entry`. Defense in depth against garbage.

### 4.3 `memtable/` — the in-RAM sorted buffer
Wraps `google/btree`. A B-tree keeps keys **sorted** and gives O(log n) insert/lookup
and cheap in-order traversal — exactly what flush needs.

- `Item` (`memtable_types.go`) is `{Type, Key, Value}`. Its `Less` method
  (`memtable.go:8`) compares by `string(Key)` so the tree orders by key.
- `Put` (`memtable.go:18`): the size bookkeeping is the clever bit. If the key
  already exists it *subtracts* the old entry's bytes before adding the new ones, so
  `size` stays an accurate running total. `ReplaceOrInsert` overwrites in place.
- `Delete` (`memtable.go:51`): does **not** remove the key. It inserts a **tombstone**
  (`Type: 1`, nil value). That's why the memtable can shadow an older on-disk value.
- `Get` (`memtable.go:34`): if it finds a tombstone, returns `(nil, false)` — deleted
  looks identical to absent to the caller.
- `Size()` drives the flush decision back in `db.Put`.
- `Ascend` walks every item in sorted order, calling your callback. This is the
  bridge to flushing — the SSTable writer calls `Ascend` to stream keys out sorted.

### 4.4 `sstable/` — immutable on-disk sorted files (the heart)
This is the biggest package. An SSTable is written once, never modified. File layout
(comments in `block.go` and `index.go`):
```
[ Block 1 ][ Block 2 ]...[ Index ][ bloomLen + bloomBytes ][ Footer ]
```

Read these files in order:

**`sstable_types.go`** — the vocabulary:
- `BlockEntry {Key, Value, Type}` — one record inside a block.
- `IndexEntry {FirstKey, Offset, Length}` — "block starting with FirstKey lives at
  byte Offset, is Length bytes long." This is the **sparse index**: one entry per
  *block*, not per key.
- `SSTable {FilePath, index []IndexEntry, bloom *BloomFilter}` — an *open* table
  keeps the index and bloom filter in RAM; the block data stays on disk.

**`block.go`** — how a block is encoded/decoded.
- `encodeBlock`: writes `numEntries`, then each entry, then a CRC32 over all of it.
- `writeBlockEntry`: `type(1) | keyLen(4) | valLen(4) | key | value` — same shape as
  a WAL record minus the outer length. Learn this once; it recurs.
- `decodeBlock`: splits off the trailing 4-byte CRC, **verifies it**, then reads
  `numEntries` and loops. If the CRC fails it errors out ("block CRC mismatch") —
  same corruption-detection philosophy as the WAL.

**`write_block_collector.go`** — a little accumulator. You feed it `BlockEntry`s; it
tracks a running `sizeEstimate` and remembers the block's first key. When the estimate
crosses `DEFAULT_BLOCK_SIZE` (4KB) the writer `drain()`s it into a finished block.
This is *how blocks get to be ~4KB*.

**`writer.go`** — `Flush(memtable, dir)`: the memtable → SSTable pipeline.
1. Create `assets/sstables/<UnixNano>.sst`. **The filename is a nanosecond timestamp**
   — this is how "newest" is later determined (bigger number = newer).
2. `memTable.Ascend(...)` streams keys **in sorted order**. For each: add to bloom,
   add to the block collector. When the collector hits 4KB, `flushBlock` writes it and
   records an `IndexEntry` (firstKey, offset, length).
3. Flush the final partial block.
4. `encodeIndex` writes all index entries.
5. Serialize the bloom filter, write `bloomLen` + bloom bytes.
6. `encodeFooterWithBloom` writes the trailer.
7. `writer.Flush()` (buffer→OS) then `file.Sync()` (OS→disk) — durable.

   `indexSize()` computes how many bytes the index will occupy so it can calculate
   `bloomOffset` without seeking. `currentOffset` is threaded through the whole thing
   as the running write position.

**`write_from_entries.go`** — the *same* writer logic but starting from a
`[]BlockEntry` instead of a memtable. Compaction uses this to write a merged file.
Note the closure `flushCurrent` (section 3.11) factoring out the repeated block-flush.
> This file and `writer.go` are ~90% duplicated — a natural refactor target (see
> exercises). Recognizing the duplication *is* re-learning the codebase.

**`index.go`** — index & footer encode/decode, plus the search.
- Footer is the **last 20 bytes**: `indexOffset(8) | numEntries(4) | bloomOffset(8)`.
  `readFooterWithBloom` seeks to `-20` from end and reads them. This is why you can
  open a huge file by reading just 20 bytes first — the footer is a map to everything
  else. (`readFooter`/`encodeFooter` are the older 12-byte, pre-bloom versions kept
  around.)
- `searchIndex` (`index.go:168`) is a **binary search** returning the block whose
  `FirstKey <= key`. Read it closely — it's the classic "rightmost value ≤ target"
  variant: it keeps moving `lo` right while `FirstKey <= key`, recording the last
  match in `result`. If your key sorts before every block's first key, it returns
  `-1` (absent).

**`sstable.go`** — the lookup path.
- `Lookup` is the money function and encodes the read strategy:
  1. `bloom.MightContain(key)` false → return `KEY_ABSENT` **without any disk I/O**.
  2. `searchIndex` → which block.
  3. `readBlock` → seek to offset, read exactly `Length` bytes, `decodeBlock`.
  4. `findInBlockLookup` → linear scan the (small) block; tombstone → `KEY_DELETED`,
     match → `KEY_FOUND`.
- Note it returns a **three-state** result (`KEY_ABSENT/FOUND/DELETED`), not a bool,
  because "deleted" must stop the search in `db.searchSSTables` (a tombstone in a
  newer table must hide a value in an older one).

**`reader.go`** — `Open(path)` reads the footer, loads the index and bloom into RAM,
returns an `*SSTable`. `OpenAllInDir` reads the directory and **sorts filenames
descending** (`Name() > Name()`) so the newest UnixNano file is first — establishing
the "index 0 = newest" invariant the read/merge paths depend on.

**`block_iterator.go`** — `NewBlockIterator` reads *every* block of a table into one
`[]BlockEntry`. Compaction uses it to stream a whole table. (It's fully in-memory
today — a known limitation for very large tables.)

**`sort_entries.go`** — `SortEntries` sorts `[]BlockEntry` by key. Used after a merge
to restore global order before writing the new file.

### 4.5 `bloomfilter/` — cheap "probably absent" test
A Bloom filter answers "is X in the set?" with **no false negatives** (if it says no,
it's truly absent) but occasional **false positives** (rare "yes" that's actually no).
Perfect for skipping SSTables that can't hold your key.

- `bloomfilter_precompute.go` — the math. Given `n` expected keys and target
  false-positive rate `p`, it computes the bit-array size `m` and hash count `k`:
  `m = -(n·ln p)/(ln2)²`, `k = (m/n)·ln2`. Standard bloom sizing formulas.
- `bloom.go` — `NewBloomFilter(expectedKeys)` uses those to size `bits []bool`.
- `hash.go` — `hashPosition` uses **double hashing**: `h(key,i) = (h1 + i·h2) mod m`,
  built from two FNV hashes. This fakes `k` independent hash functions from just two,
  a well-known trick. Also here: `Serialize`/`Deserialize`, which **pack the bool
  array into real bits** (8 per byte) with an 8-byte header (`size`, `numHash`) so the
  filter can live on disk compactly and be reloaded on `Open`.
- `bloomfilter_operations.go` — `Add` sets `k` bits; `MightContain` returns false the
  instant *any* of the `k` bits is 0. That early-false is the whole speed win.

### 4.6 `compaction/` — merging SSTables
Too many SSTables = slow reads (a miss might probe them all). Compaction merges
similar-sized files into one, dropping duplicate keys (newest wins).

- `compact.go` — `Compact(tables, dir)`: if fewer than 4 tables, do nothing. Else
  group by size, merge each group of ≥2, delete the source files, return the new list.
- `group_by_similar_size.go` — greedy grouping. `os.Stat` each file's size, then bucket
  files whose sizes are within the 2× ratio together (size-tiered compaction).
- `is_similar_size.go` — the ratio test: bigger/smaller ≤ 2.
- `merge.go` — the actual merge:
  - `mergeGroup` builds a `BlockIterator` per table and calls `mergeIterators`.
  - `mergeIterators` walks tables **in order (newest first)** keeping a `seen` set;
    the first time it sees a key it keeps that entry and ignores later (older)
    duplicates → **newest-wins**. Then `SortEntries` re-sorts and it writes one new
    file via `WriteSSTableFromBlockEntries`.
  - **Tombstones are intentionally preserved** (comment at `merge.go:39`). Dropping a
    delete-marker early could let an older SSTable "resurrect" the key. Safe only with
    a manifest tracking full-merge coverage, which hcdb doesn't have yet.

### 4.7 `db/` — the public façade tying it together
- `db_types.go` — the `DB` struct: a WAL, a memtable, a slice of SSTables, a config,
  and an `RWMutex`.
- `db.go`:
  - `Open` — mkdir the SST dir, open the WAL, **replay the WAL to rebuild the
    memtable** (crash recovery!), open all SSTables. This is where a restart recovers
    everything.
  - `rebuildMemtable` — replays WAL entries, applying PUTs and DELETEs to a fresh
    memtable. (Anything already flushed to SSTables isn't in the WAL, because…)
  - `flushMemtable` — under the write lock: flush memtable → new SSTable, prepend it,
    reset the memtable, **reset the WAL** (`resetWAL` truncates it to empty since those
    writes are now durable in the SSTable), then run compaction.
  - `searchSSTables` — walk tables newest→oldest; first `KEY_FOUND` wins, first
    `KEY_DELETED` means stop and report absent.
  - `Close` — sync the WAL.
- `db_operations.go` — the API you actually call: `Get`, `Put`, `Delete`. `Put` writes
  WAL→memtable→maybe-flush. `Get` checks memtable then SSTables. Small and readable —
  the complexity lives below.

### 4.8 `main.go` — the demo
The big commented-out block is old scratch code (`write`/`replay` experiments) —
harmless history. `main()` opens the DB at `assets/main.wal` + `assets/sstables`, does
four batches each ending in `ForceFlush()` (the 4th crosses the compaction threshold),
then reads back keys to demonstrate latest-wins and deletes. `printGet` is a helper.

---

<a name="5-end-to-end-traces"></a>
## 5. Four end-to-end traces

Follow these with the files open — this is where it "clicks."

### Trace A — `database.Put("harsh","chauhan")`
1. `db_operations.go` `Put` → `db.wal.Put("harsh","chauhan")`.
2. `wal/operators.go` builds `Entry{Key, Value, Type:OP_PUT}` → `Append`.
3. `wal/append.go` serializes + CRC + writes `[len][data][crc]` to the buffered file.
   Counter hits 10 → `Sync()` forces it to disk.
4. Back in `Put`: `db.memtable.Put([]byte("harsh"), []byte("chauhan"))` inserts into
   the B-tree, updating `size`.
5. `db.memtable.Size() >= 4MB`? Not yet → return nil. Write done, durable in WAL.

### Trace B — `database.Get("harsh")` (memtable hit)
1. `Get` takes `RLock`.
2. `db.memtable.Get([]byte("harsh"))` finds the item, not a tombstone → returns
   `("chauhan", true)` immediately. **Zero disk I/O.** This is why memtable reads are
   ~66× faster than SSTable reads in the benchmarks.

### Trace C — `database.Get("c")` after a flush (SSTable hit)
1. Memtable miss → `searchSSTables([]byte("c"))`.
2. For each SSTable newest→oldest: `sst.Lookup`.
3. `bloom.MightContain("c")`: newest table that holds "c" says yes.
4. `searchIndex` binary-searches the in-RAM index → block index.
5. `readBlock` seeks to that block's offset, reads `Length` bytes, verifies CRC,
   decodes entries.
6. `findInBlockLookup` scans the block, finds "c", returns `("3", KEY_FOUND)`.

### Trace D — the 4th `ForceFlush()` triggers compaction
1. `flushMemtable` writes SSTable #4 and prepends it (now 4 tables).
2. `compaction.Compact`: 4 ≥ threshold → proceed.
3. `groupBySimilarSize`: the four small equal-ish files land in one group.
4. `mergeGroup`: iterate all four newest→oldest, dedupe by key (newest wins),
   preserve tombstones (so deleted `"b"` stays hidden), sort, write one new `.sst`.
5. Delete the four source files. `db.sstables` becomes the single merged table.
6. Later `Get("b")` finds the tombstone → "not found." Delete survived the merge.

### Bonus — crash recovery
Kill the process mid-run and restart. `db.Open` → `wal.Open` → `Replay` reads the WAL;
any half-written tail record fails CRC and is truncated; the clean prefix rebuilds the
memtable. Already-flushed data is safe in SSTables (their WAL was reset at flush).
You're back to a consistent state.

---

<a name="6-idioms-and-gotchas"></a>
## 6. Go idioms & gotchas living in this codebase

- **Buffered writer + explicit Sync.** `bufio.Writer` batches writes in memory; nothing
  is on disk until `Flush()`. `Flush()` only pushes to the OS; `File.Sync()` pushes the
  OS cache to the physical device. Durability needs **both** (`wal/sync.go`,
  `sstable/writer.go`). Forgetting the second one is a classic "my data vanished on
  power loss" bug.
- **Symmetric serialization.** Every `binary.Write(...LittleEndian, x)` must be mirrored
  by a `binary.Read(...LittleEndian, &x)` in the same field order. When you change a
  disk format, change *both sides* or you'll read garbage. Old files also become
  unreadable — there's no format versioning yet.
- **`&x` in `binary.Read`.** You pass the *address* so the function writes into your
  variable. Forgetting the `&` is a common mistake.
- **Slice prepend via spread.** `append([]T{new}, old...)` = prepend. Used to keep
  newest-first ordering. Reused in the "newest wins" logic in compaction and reads.
- **Type assertions with the btree.** `i.(Item)` unwraps the interface back to your
  concrete type. Safe here because only `Item`s are ever inserted.
- **`time.Now().UnixNano()` as filename = ordering.** Clever and simple, but two flushes
  in the same nanosecond would collide (extremely unlikely at this scale; still a real
  edge case).
- **Value vs pointer receiver.** `Item.Less` is a value receiver (read-only, tiny);
  everything that mutates uses pointer receivers. Getting this wrong = silently editing
  a copy.
- **Locking asymmetry (see 3.8).** `Get`/flush use `db.mu`; `Put`/`Delete` lean on the
  memtable's own lock. Fine for isolated ops, worth auditing under real concurrency —
  run `go test ./... -race`.

---

<a name="7-exercises"></a>
## 7. Exercises to get sharp again

Ordered easy → hard. Each one forces you to touch a different Go concept.

1. **Read-only warmup.** Add a `func (db *DB) Stats()` that returns the memtable size
   and SSTable count. Teaches: struct access, method receivers, multiple returns.
2. **Range scan.** Add `func (db *DB) Scan(prefix string) []string` returning all keys
   with that prefix from the memtable (`Ascend` + `strings.HasPrefix`). Teaches:
   closures, slices, the callback pattern.
3. **Write a test.** Create `db/db_test.go` with a `TestPutGetDelete` using
   `t.TempDir()` for isolation. Run `go test ./db/`. Teaches: the testing package,
   table-driven tests.
4. **Kill the duplication.** `writer.go` and `write_from_entries.go` share ~90% of their
   logic. Extract the common block-writing loop into one function both call. Teaches:
   refactoring, closures, interfaces (`io.Writer`).
5. **Filename collision fix.** Make SSTable filenames robust when two flushes hit the
   same nanosecond (append a counter). Teaches: state, formatting, edge cases.
6. **Streaming k-way merge (advanced).** Replace the in-memory merge in `compaction`
   with a min-heap over block iterators (`container/heap`). Teaches: interfaces (heap
   requires 5 methods), iterators, bounded memory. This is item #3 on the README's
   roadmap — a real contribution to your own project.

Do 1–3 this week to rebuild reflexes; 4–6 when you want the engine itself to improve.

---

## One-paragraph summary to hold in your head

hcdb writes every mutation to an append-only **WAL** (crash safety), applies it to an
in-RAM sorted **B-tree memtable** (fast writes), and when that fills, flushes it to an
immutable sorted **SSTable** file (blocks + sparse index + bloom filter). Reads check
the memtable, then SSTables newest→oldest, using each file's **bloom filter** to skip
misses and its **index** to read just one 4KB block. Deletes are **tombstones** that
flow through everything. When SSTables pile up, **size-tiered compaction** merges them,
newest-wins, preserving tombstones. Everything on disk is **CRC32-checked** and
**little-endian**, and the WAL truncates a corrupt tail on replay. That's the whole
engine — and re-reading the code with those seven bold words in mind is how you get
your Go instincts back.
```
