# hcdb — A Persistent Key-Value Store in Go

A storage engine built from scratch to understand how real databases work internally.
Implements the core ideas behind LevelDB and RocksDB: WAL, memtable, SSTables,
block-based layout, and size-tiered compaction.

**This is a learning project, not production software.**
The goal was depth of understanding, not feature completeness.

---

## Architecture

```
Write Path
──────────
  Put(key, value)
       │
       ▼
  WAL (append-only, CRC32 protected)          ← crash safety
       │
       ▼
  Memtable (BTree, sorted, in-memory)         ← fast writes
       │
       │  when size >= 4MB
       ▼
  SSTable (immutable, sorted, on disk)        ← durable storage
       │
       │  when SSTable count >= 4
       ▼
  Compaction (size-tiered merge)              ← reclaim space, remove tombstones

Read Path
─────────
  Get(key)
       │
       ▼
  Memtable lookup  ──── found? → return value
       │
       │ not found
       ▼
  SSTable scan (newest → oldest)
       │
       ▼
  Binary search on index → read block → scan block entries
```

---

## On-Disk Format

### SSTable File Layout

```
┌─────────────────────────────────────┐
│  Block 1  (~4KB)                    │
│  Block 2  (~4KB)                    │
│  ...                                │
├─────────────────────────────────────┤
│  Index Section                      │
│  (firstKey, offset, length) × N     │
├─────────────────────────────────────┤
│  Footer (12 bytes)                  │
│  indexOffset (8B) | numEntries (4B) │
└─────────────────────────────────────┘
```

### Block Layout

```
┌────────────────────────────────────────────────────────────┐
│ numEntries (4 bytes)                                       │
│ entry 1: type(1) | keyLen(4) | valLen(4) | key | value    │
│ entry 2: ...                                               │
│ ...                                                        │
│ CRC32 checksum (4 bytes)                                   │
└────────────────────────────────────────────────────────────┘
```

Each block is ~4KB — aligned with OS page size. Small enough for I/O efficiency,
large enough to amortize index overhead.

### WAL Entry Layout

```
┌──────────────────────────────────────────────────────────────────────┐
│ totalLength(4B) | type(1) | keyLen(4) | valLen(4) | key | val | CRC32(4) │
└──────────────────────────────────────────────────────────────────────┘
```

CRC32 on every WAL entry. A corrupted tail (incomplete write on crash)
is truncated cleanly on replay — the DB opens successfully with the last
clean state intact.

---

## Design Decisions and Why

### Why LSM-tree instead of a pure B-tree?

B-trees do in-place updates — random writes to disk. LSM-trees convert random
writes into sequential appends (WAL + SSTable flush). Sequential I/O is
significantly faster because it eliminates seek time on spinning disk and reduces
write amplification on SSDs.

The trade-off: reads pay more (must check memtable + multiple SSTables before
finding a key). This is the fundamental LSM read-write trade-off — optimise for
writes, accept higher read cost, use compaction to bound it.

### Why a sparse index instead of a full index?

A full index (one entry per key) would require loading the entire index into memory
and grows proportionally with the dataset. A sparse index (one entry per ~4KB block)
means the index fits comfortably in memory even for large SSTables. Binary search
on the index narrows the lookup to one block, then a linear scan within the block
finds the key. Trade-off: must scan up to 4KB per lookup. Acceptable given block
size aligns with OS page size — the entire block is likely in one page read.

### Why CRC32 on every block and WAL entry?

Silent data corruption is real — storage devices flip bits. Without checksums a
corrupted block looks like valid data and silently returns wrong results. CRC32 is
hardware-accelerated on modern CPUs and catches the vast majority of corruption
patterns. Cost is negligible; benefit is correctness guarantees.

### Why size-tiered compaction?

Simplest strategy that works correctly. SSTables of similar size are grouped and
merged. Write amplification is lower than leveled compaction. Read amplification
is higher because there is no bound on SSTable count per level — a miss must scan
all SSTables. Leveled compaction (LevelDB, RocksDB) bounds read amplification at
the cost of higher write amplification. That is the natural next step.

### Why BTree for the memtable?

The memtable must be sorted — SSTable flush requires sorted output for binary
search to work on disk. BTree gives O(log n) insert and O(n) ordered traversal,
exactly what the flush path needs. Skip list is the alternative used by LevelDB —
similar asymptotic complexity, better concurrent insert performance, more complex
to implement correctly.

### Why tombstones are preserved through compaction

A tombstone (delete marker) cannot be dropped during compaction unless we can
guarantee no older SSTable at any level still contains the key. Without a manifest
tracking which files have been fully merged, dropping a tombstone early causes
deleted keys to resurrect from older SSTables on the next read. This engine
preserves tombstones through compaction as the safe default.

---

## Benchmarks

Run on: Intel i7-1165G7 @ 2.80GHz, Linux, amd64
Command: `go test ./benchmark/ -bench=. -benchtime=5s -benchmem`

### Core Operations

| Benchmark                    | ops/sec    | ns/op  | allocs/op | Notes                               |
| ---------------------------- | ---------- | ------ | --------- | ----------------------------------- |
| PutSequential                | ~220,000   | 4,543  | 48        | WAL append + memtable insert        |
| PutRandom                    | ~223,000   | 4,485  | 38        | Memtable absorbs random write order |
| GetMemtableHit               | ~2,480,000 | 403    | 3         | No disk I/O — pure BTree lookup     |
| GetSSTableHit                | ~38,800    | 25,763 | 798       | Index search + block read + decode  |
| GetMiss (key absent)         | ~96,100    | 10,406 | 241       | Scans all SSTables — see note below |
| Delete                       | ~757,500   | 1,320  | 12        | Tombstone write — smaller payload   |
| Mixed (80% write / 20% read) | ~155,700   | 6,425  | 137       | Realistic workload approximation    |
| CompactionThroughput         | ~87,700    | 11,394 | 121       | Flush + compaction every 500 ops    |

### Miss Latency vs SSTable Count

This benchmark isolates how read-miss cost grows as SSTable count increases.
It is the clearest argument for why Bloom filters matter in an LSM engine.

| SSTable Count | ns/op  | allocs/op | Observation                                           |
| ------------- | ------ | --------- | ----------------------------------------------------- |
| 1             | 16,755 | 516       | Baseline — one file to check                          |
| 5             | 20,561 | 548       | Moderate growth — 5 files scanned                     |
| 10            | 37,681 | 1,085     | ~2x cost of 5 SSTables                                |
| 20            | 37,812 | 1,090     | Nearly identical to 10 — compaction merged files down |

**Why 10 and 20 SSTables show the same latency:**
Size-tiered compaction fires when SSTable count reaches the threshold (4 by default).
By the time the benchmark seeds 20 SSTables, compaction has already merged them down
to approximately 10. This is compaction working correctly — it bounds SSTable count
under sustained write load. The practical implication: miss latency is bounded by
the post-compaction SSTable count, not the raw write volume.

**The Bloom filter argument:**
Even bounded at ~10 SSTables, a miss costs ~37μs and touches ~1,085 allocations.
A per-SSTable Bloom filter would reject ~99% of misses with a single probabilistic
check and zero disk reads, making miss latency effectively O(1) regardless of
SSTable count.

**Memtable vs SSTable read gap: ~64x**
GetMemtableHit at 403 ns vs GetSSTableHit at 25,763 ns. This gap represents the
combined cost of disk I/O, index binary search, block decode, and allocation
overhead. It is the core reason LSM engines use a large memtable — keep hot data
in memory as long as possible.

**On GetSSTableHit (798 allocs/op):**
Block decoding allocates a `[]byte` per key and per value for every entry read.
At 798 allocations per lookup, GC pressure will dominate under high read throughput.
A production engine would use a block cache with pre-allocated byte buffers and
arena allocation to eliminate most of these. This is a known limitation of the
current implementation.

---

## Known Limitations

These are understood design gaps, not surprises. Each points to a concrete
next implementation step.

**1. No Bloom Filters**
Negative lookups scan all SSTables. The scaled miss benchmark shows the cost
clearly. Next step: per-SSTable Bloom filter persisted in the SSTable file,
loaded into memory on open. False positive rate ~1% eliminates ~99% of disk
reads on misses.

**2. No Manifest File**
SSTable state is reconstructed from the filesystem on open. A crash mid-compaction
could leave orphaned `.sst` files with no record of which are valid. Production
engines (LevelDB, RocksDB) use a MANIFEST file to record atomic version
transitions — compaction is a logged operation, not just a file rename.

**3. In-Memory Compaction**
Current compaction loads all entries from all SSTables into memory before merging.
Correct approach: streaming k-way merge using a min-heap — O(k log k) time,
bounded memory regardless of SSTable size. Required before this engine can handle
SSTables larger than available RAM.

**4. No Leveled Compaction**
Size-tiered compaction gives no hard bound on read amplification. Leveled
compaction organises SSTables into levels with size ratios, bounding read
amplification to O(number of levels) regardless of write volume.

**5. WAL Sync Policy**
WAL syncs every N operations (N=10 by default). Between syncs, up to N-1
acknowledged writes can be lost on power failure. This is an explicit
durability trade-off — identical to MySQL's `innodb_flush_log_at_trx_commit=2`.
Sync-on-every-write would make Put fully durable but ~10x slower.

**6. Single-Threaded Compaction**
Compaction runs synchronously inside `flushMemtable`. Under heavy write load
this introduces p99 latency spikes proportional to compaction time. Production
engines run compaction on a background goroutine with write throttling when
compaction debt accumulates.

**7. High Allocation Count on Block Decode**
798 allocs/op on SSTableHit. Each block decode allocates fresh byte slices per
entry. A buffer pool or arena allocator would reduce this significantly.

---

## What I Learned Building This

**WAL ordering is not optional.**
WAL must be written before the memtable update, not after. If the process crashes
between a memtable write and its WAL entry, the write is silently lost with no
recovery path. The order is load-bearing.

**Tombstone semantics are subtle.**
A tombstone cannot be dropped during compaction unless all older SSTables that
could contain the key have also been compacted into the same output. Drop it too
early and the deleted key resurrects from an older file on the next read. Getting
this wrong produces a database that silently un-deletes data.

**Read amplification compounds quickly.**
With 10 SSTables and no Bloom filter, every miss touches 10 files. With 100,
it touches 100. Compaction is not a maintenance task — it is the mechanism that
keeps read amplification from becoming the dominant cost.

**Block size is a genuine trade-off.**
Smaller blocks mean more index entries and more memory for the index, but better
I/O granularity — you read exactly what you need. Larger blocks mean fewer index
entries but wasteful reads when only one key is needed from the block. 4KB aligns
with the OS page size — reading one block is one page fault. Not accidental.

**CRC corruption must be handled at startup.**
A corrupted WAL tail from an incomplete write on crash should truncate cleanly,
not prevent the DB from opening. Treating a partial write as fatal makes the
engine unrecoverable from the most common failure mode. The WAL replay path
truncates at the first corrupt entry and continues with the valid prefix.

**Compaction correctness depends on SSTable ordering.**
The merge logic assumes the newest SSTable wins on duplicate keys. That invariant
must be enforced by whoever constructs the SSTable list — if filesystem iteration
order is used without explicit sorting by timestamp, the invariant can silently
break and older values overwrite newer ones.

---

## What's Next

In priority order, what a production version of this engine would require:

1. Bloom filters per SSTable — fix read miss performance, O(1) negative lookups
2. Manifest file — atomic compaction, safe crash recovery
3. Streaming k-way merge — bounded memory compaction for large SSTables
4. Leveled compaction — bound read amplification regardless of write volume
5. Background compaction goroutine — eliminate p99 latency spikes on flush
6. Block cache with buffer pool — eliminate 798 allocs/op on SSTable reads
7. Snapshot reads / MVCC — foundation for transaction semantics

---

## Project Structure

```
hcdb/
├── config/         — constants and DB config struct
├── wal/            — write-ahead log: append, CRC32, replay, truncation
├── memtable/       — in-memory sorted BTree with RWMutex
├── sstable/        — on-disk format: blocks, sparse index, footer, lookup
├── compaction/     — size-tiered merge: grouping, k-way merge, file cleanup
├── db/             — public API: Get, Put, Delete, Open, Close, ForceFlush
└── benchmark/      — Go benchmark suite with scaled miss analysis
```

---

## Running

```bash
git clone https://github.com/hchauhan7816/hcdb
cd hcdb
go run main.go

# run full benchmark suite
go test ./benchmark/ -bench=. -benchtime=5s -benchmem

# run scaled miss benchmark only
go test ./benchmark/ -bench=BenchmarkGetMissScaled -benchtime=3s -benchmem

# run with race detector
go test ./... -race
```
