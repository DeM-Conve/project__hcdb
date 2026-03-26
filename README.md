# hcdb — A Persistent Key-Value Store in Go

**hcdb** is an in-progress LSM-style embedded key-value engine in Go: WAL,
memtable, SSTables with a block-based layout, and size-tiered compaction - the
same architectural pillars as LevelDB and RocksDB. It is under active
development; the sections below document how it works today and what is still
on the roadmap toward production-grade behavior.

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
  Memtable (B-tree, sorted, in-memory)        ← fast writes
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
  Bloom check → maybe absent? skip SSTable
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
│  Bloom Section                      │
│  bloomLen (4B) | bloomBytes (N bytes) │
├─────────────────────────────────────┤
│  Footer (20 bytes)                  │
│  indexOffset (8B) | numEntries (4B) │
│  bloomOffset (8B)                   │
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

### Why WAL sync is batched (every N operations)

The WAL syncs to disk every N operations (N=10 by default). Between syncs, up
to N-1 acknowledged writes can be lost on a hard power failure. That is a
deliberate **durability vs throughput** trade-off—similar in spirit to MySQL’s
`innodb_flush_log_at_trx_commit=2`: you accept a bounded loss window for much
higher sustained write rates. Sync-on-every-write would make `Put` fully durable
on every ack but is roughly an order of magnitude slower in typical setups.

---

## Benchmarks

Run on: Intel i7-1165G7 @ 2.80GHz, Linux, amd64
Command: `go test ./benchmark/ -bench=. -benchtime=5s -benchmem`

### Core Operations

| Benchmark                    | ops/sec    | ns/op   | allocs/op | Notes                               |
| ---------------------------- | ---------- | ------- | --------- | ----------------------------------- |
| PutSequential                | 2,291,530  | 5,177   | 49        | WAL append + memtable insert        |
| PutRandom                    | 1,326,667  | 5,365   | 39        | Memtable absorbs random write order |
| GetMemtableHit               | 16,069,924 | 362.6   | 3         | No disk I/O — pure BTree lookup     |
| GetSSTableHit                | 247,140    | 23,825  | 798       | Index search + block read + decode  |
| GetMiss (key absent)         | 27,245,608 | 210.8   | 4         | Bloom reject path for most misses   |
| Delete                       | 4,751,407  | 1,377   | 12        | Tombstone write — smaller payload   |
| Mixed (80% write / 20% read) | 1,249,740  | 6,195   | 136       | Realistic workload approximation    |
| CompactionThroughput         | 930,319    | 20,187  | 175       | Flush + compaction every 500 ops    |

### Miss Latency vs SSTable Count

This benchmark isolates how read-miss cost grows as SSTable count increases.
It shows how stable miss latency remains with Bloom filters enabled.

| SSTable Count | ns/op | allocs/op | Observation                                                    |
| ------------- | ----- | --------- | -------------------------------------------------------------- |
| 1             | 241.5 | 4         | Bloom rejects most misses before block I/O                     |
| 5             | 272.5 | 4         | Small overhead from checking more in-memory Bloom filters      |
| 10            | 232.7 | 4         | Similar miss latency; Bloom path keeps misses near-constant    |
| 20            | 278.4 | 4         | Still bounded; no linear growth with table count in this range |

**Why miss latency stays flat from 1 to 20 SSTables:**
With per-SSTable Bloom filters, most misses are rejected from in-memory bit tests
before any block read. As SSTable count increases, miss cost remains close to
constant in this benchmark (roughly 0.21-0.28 us), instead of rising with file count.

**Bloom filter impact in the current implementation:**
Misses now complete in ~211–278 ns with just 4 allocations/op across 1–20 SSTables.
This reflects the intended Bloom fast path: reject negative lookups cheaply, avoid
disk block reads on misses, and keep miss behavior effectively near O(1) in practice.

**Memtable vs SSTable read gap: ~66x**
GetMemtableHit at 362.6 ns vs GetSSTableHit at 23,825 ns. This gap represents the
combined cost of disk I/O, index binary search, block decode, and allocation
overhead. It is the core reason LSM engines use a large memtable — keep hot data
in memory as long as possible.

**On GetSSTableHit (798 allocs/op):**
Block decoding allocates a `[]byte` per key and per value for every entry read.
At 798 allocations per lookup, GC pressure will dominate under high read throughput.
A mature implementation would use a block cache with pre-allocated byte buffers
and arena allocation to eliminate most of these; hcdb does not yet.

---

## Known Limitations

These are **implementation gaps** in the current codebase—missing features or
scaling bounds—not policy trade-offs already described under *Design Decisions
and Why* (for example batched WAL sync). Each item points to a concrete next step.

**1. No Bloom filter metrics/tuning yet**
Bloom filters are now present per SSTable, but they are not yet configurable
per workload (expected keys / false-positive rate) and are not benchmarked as a
separate before-vs-after profile in this README.

**2. In-memory compaction**
Compaction loads all entries from the SSTables in a group into memory before
merging. A streaming k-way merge with a min-heap gives O(k log k) work and
bounded RAM regardless of SSTable size—required before very large tables are safe.

**3. Single-threaded compaction**
Compaction runs synchronously inside `flushMemtable`. Under heavy writes this
inflates tail latency. Background compaction with throttling when debt builds is
the usual next step.

**4. High allocation count on block decode**
~798 allocs/op on SSTable hits in benchmarks: each decode allocates fresh slices
per entry. A buffer pool or arena would cut GC pressure sharply.

---

## Key ideas this engine illustrates

**WAL before memtable — ordering is load-bearing.**  
The WAL entry must be persisted before the in-memory update. A crash between
memtable write and WAL record loses the write with no recovery path.

**Tombstones and compaction — correctness is easy to get wrong.**  
A tombstone is unsafe to drop until no older SSTable can still hold the key;
otherwise a deleted key can reappear on read. Wrong merge rules silently
“un-delete” data.

**Read amplification grows with SSTable count (without Bloom filters).**  
Without Bloom filters, every miss probes every SSTable. Compaction is the
mechanism that keeps that cost from dominating; it is not optional housekeeping.

**Block size balances index size vs read granularity.**  
Smaller blocks improve I/O precision; larger blocks shrink the index but may
pull in unused data. ~4KB lines up with typical OS pages so one block read maps
cleanly to a page fault.

**Treat a corrupt WAL tail as truncatable, not fatal.**  
Incomplete writes on crash should be discarded from the tail so the database
still opens with a consistent prefix. Replay stops at the first bad entry.

**Merge correctness needs explicit newest-wins ordering.**  
Compaction assumes the newest SSTable wins on duplicate keys. Building the file
list from arbitrary directory order without a stable “newest first” rule can let
older values overwrite newer ones; this engine now enforces newest-first ordering
when opening SSTables.

---

## What's Next

Rough priority order for hardening and extending hcdb:

1. **Bloom filter tuning + measurement** — configurable false-positive targets and explicit miss-latency impact benchmarks
2. **Manifest / versioned metadata** — atomic compaction and clearer crash recovery
3. **Streaming k-way merge** — bounded-memory compaction for large SSTables
4. **Leveled (or hybrid) compaction** — stronger bounds on read amplification vs today’s size-tiered baseline
5. **Background compaction** — decouple flush latency from merge work; throttle when debt grows
6. **Block cache and buffer reuse** — cut allocation churn on hot read paths
7. **Snapshots / MVCC** — basis for richer isolation and transactional semantics

---

## Project Structure

```
hcdb/
├── main.go              — small demo / entrypoint
├── go.mod, go.sum       — Go module (github.com/hchauhan7816/hcdb)
├── Dockerfile           — container image for the demo
├── docker-compose.yml   — optional compose wiring
├── README.md
├── .gitignore
├── config/              — defaults and DB config struct
├── wal/                 — append-only log, CRC32, replay, truncation, sync policy
├── memtable/            — in-memory sorted B-tree (google/btree), RWMutex
├── sstable/             — blocks, sparse index, footer, read/write paths
├── bloomfilter/         — Bloom filter types, hashing, serialization, set ops
├── compaction/          — size-tiered grouping, merge, file lifecycle
├── db/                  — Open/Close, Get, Put, Delete, ForceFlush
└── benchmark/           — Go benchmarks including scaled miss analysis
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
