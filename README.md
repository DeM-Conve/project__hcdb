# HCDB — A Durable LSM-Tree Database, Speaking the Redis Protocol

**HCDB** is an LSM/SSTable-based key-value database, built the same way LevelDB and RocksDB
are — WAL, memtable, block-based SSTables, size-tiered compaction, MVCC snapshots — but it talks
to your application over RESP, the wire protocol Redis already speaks. Point `redis-cli`, or any
Redis client library your stack already has, straight at it. No new driver, no new query
language, no new mental model.

The difference that matters: **HCDB doesn't lose data when the process dies.** Every write is
WAL-logged and `fsync`'d before it's acknowledged, and every SSTable is installed atomically
(temp file + rename + directory fsync) — so where an in-memory store trades durability for
speed, HCDB is durable *and* fast where it counts: reads. Hot-key reads run at 209 ns/op,
key-miss rejection at 81 ns/op, and a sharded LRU block cache turns a 21,000 ns cold SSTable
read into a 560 ns warm one — a measured 37× speedup, on real disk, not tmpfs.

It is under active development; the sections below document how it works today and what is
still on the roadmap toward production-grade behavior. For a per-package deep dive, see
[docs/](docs/).

---

## Demo

https://github.com/user-attachments/assets/16afab77-28fc-4c9f-b8f7-0c5ff18de86f

---

## Why HCDB

- **Drop-in for anything that already speaks Redis.** Native RESP support means `redis-cli` and
  every existing Redis client library/driver works against it with zero migration.
- **Actually durable, not "eventually persistent."** Every write goes through a WAL with `fsync`
  *before* it's acknowledged — unlike Redis's in-memory-first, snapshot-on-top model.
- **Crash-safety that's tested, not assumed.** A dedicated fault-injection suite simulates torn
  writes (process dying mid-`write()`) and proves recovery reconstructs correct state.
- **Atomic file installs, always.** Every SSTable write is temp-file + rename + directory fsync,
  so a crash never leaves a half-written data file behind.
- **Sub-microsecond reads on hot data.** 209 ns/op for memtable hits, 81 ns/op for key-miss
  rejection via Bloom filters — measured on real disk (see [Benchmarks](#benchmarks)).
- **Reads don't get slower as your data grows.** Bloom filters keep miss latency flat
  (~90–120 ns) whether there's 1 SSTable or 20.
- **A 37× cache speedup, independently measured.** The sharded LRU block cache turns a
  21,000 ns cold SSTable read into a 560 ns warm one.
- **Consistent reads without blocking writers.** MVCC snapshots via sequence-number watermarks —
  the same mechanism LevelDB/RocksDB use for repeatable-read scans.
- **A durability/throughput dial you control**, not one hidden behind a default — the same
  trade-off MySQL exposes via `innodb_flush_log_at_trx_commit`.
- **Small enough to actually read.** Every subsystem (WAL, memtable, SSTables, compaction,
  cache, RESP layer) has its own developer doc explaining *why* it's built that way — you're not
  trusting a black box with your data.

> **Where HCDB is today:** write throughput is deliberately fsync-bound (~27k ops/sec) in favor
> of durability over raw write speed, and there is no clustering, no manifest file yet, and only
> a subset of the RESP command surface. See [What's Next](#whats-next) for the roadmap.

---

## Project Structure

- `go.mod`, `go.sum` — Go module (`github.com/DeM-Conve/project__hcdb`)
- `Dockerfile`, `docker-compose.yml`, `.env.example`, `.dockerignore` — container image and
  compose wiring for `cmd/hcdb-server` (see [docs/deployment.md](docs/deployment.md))
- `config/` — defaults and DB config struct
- `internal/base/` — internal key format (user key + sequence number + kind), MVCC comparison
  rules, and the snapshot `Watermark` compaction reads to know what's still in use
- `wal/` — append-only log, CRC32, replay, truncation, sync policy
- `memtable/` — in-memory sorted B-tree (`google/btree`) of internal keys, RWMutex, range iterator
- `sstable/` — blocks, sparse index, footer, atomic install, read/write/iterator paths
- `bloomfilter/` — Bloom filter types, hashing, serialization, set ops
- `compaction/` — size-tiered grouping, snapshot-aware merge, file lifecycle
- `cache/` — shared sharded LRU block cache (hashmap + doubly-linked list per shard)
- `resp/` — RESP wire protocol: parsing and serializing the five RESP value types
- `server/` — RESP-speaking TCP server dispatching SET/GET/DEL/SCAN/PING onto `db.DB`
- `cmd/hcdb-server/` — server entrypoint, configured via `HCDB_ADDR`/`HCDB_DATA_DIR`
- `faultinjection/` — torn-write simulation used by crash-recovery tests
- `db/` — `Open`/`Close`, `Get`/`Put`/`Delete`/`ForceFlush`, `GetSnapshot`/`ReleaseSnapshot`,
  `GetAt`/`ScanAt` (k-way merge), `Get`/`Scan` as thin latest-version wrappers over them
- `bench/` — Go benchmarks including scaled miss analysis
- `docs/` — per-package developer guides, one file per package above

---

## Running

```bash
git clone https://github.com/DeM-Conve/project__hcdb
cd project__hcdb

# run the full test suite
go test ./...

# run full benchmark suite
go test ./bench/ -bench=. -benchtime=5s -benchmem

# run scaled miss benchmark only
go test ./bench/ -bench=BenchmarkGetMissScaled -benchtime=3s -benchmem

# run with race detector
go test ./... -race
```

HCDB is a library (`github.com/DeM-Conve/project__hcdb`), not a binary you run directly — import
`db` and call `db.Open`/`Put`/`Get`/`Scan` as shown in the runnable usage example in
[db/example_test.go](db/example_test.go) (`go test ./db/ -run Example -v`), or run it as a
network server via `cmd/hcdb-server` below.

### Running as a server

To actually talk to HCDB over the network, run `cmd/hcdb-server`, which speaks RESP (the Redis
wire protocol — see [docs/resp.md](docs/resp.md) and [docs/server.md](docs/server.md)):

```bash
go run ./cmd/hcdb-server
redis-cli -p 6380 SET foo bar
redis-cli -p 6380 GET foo
redis-cli -p 6380 SCAN a z
```

Or with Docker, which also runs a RESP-aware healthcheck (see
[docs/deployment.md](docs/deployment.md)):

```bash
docker compose up --build
docker compose --profile tools run --rm redis-cli SET foo bar
```

`HCDB_ADDR` (default `:6380`) and `HCDB_DATA_DIR` (default `assets`, or `/data` inside the
container) configure the listen address and storage location without a rebuild.

---

## Architecture

```
Write Path
──────────
  Put(key, value)
       │
       ▼
  encodeNextKey: user key + fresh sequence number + kind → internal key
       │
       ▼
  WAL (append-only, CRC32 protected)          ← crash safety
       │
       ▼
  Memtable (B-tree of internal keys, sorted, in-memory)   ← fast writes, versions kept, not overwritten
       │
       │  when size >= 4MB (checked on both Put and Delete)
       ▼
  SSTable (immutable, sorted, on disk)        ← durable storage, installed atomically
       │
       │  when SSTable count >= 4
       ▼
  Compaction (size-tiered merge, keeps every version an active snapshot might still need)

Read Path
─────────
  Get(key) = GetAt(key, latest)
       │
       ▼
  Memtable lookup (newest version visible at the snapshot) ── found? → return value
       │
       │ not found
       ▼
  SSTable scan (newest → oldest)
       │
       ▼
  Bloom check → maybe absent? skip SSTable
       │
       ▼
  Seek on index → read block (through shared sharded cache) → newest visible version wins,
  a tombstone at any level stops the search

Range Scan
──────────
  Scan(lowerBound, upperBound) = ScanAt(lowerBound, upperBound, latest)
       │
       ▼
  k-way merge over memtable + every SSTable (min-heap), each source pre-filtered to the snapshot
       │
       ▼
  newest-wins on duplicate keys, tombstones hidden → sorted results

Snapshots
─────────
  GetSnapshot() pins the current sequence number (db.watermark.Begin) ── ReleaseSnapshot() unpins it
       │
       ▼
  GetAt/ScanAt with that sequence number see a consistent, unmoving view of the database
  even as later writes and flushes continue; compaction keeps whatever a pinned snapshot
  still needs (see Compaction above) until it's released
```

The full mechanics of every path — every function involved, every ordering guarantee, and why
each step is ordered the way it is — are in [docs/db.md](docs/db.md). The network-facing layer
built on top of this (RESP wire protocol + TCP server) is in
[docs/resp.md](docs/resp.md) and [docs/server.md](docs/server.md).

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
│  (firstInternalKey, offset, len)×N  │
├─────────────────────────────────────┤
│  Bloom Section                      │
│  bloomLen (4B) | bloomBytes (N bytes) │
├─────────────────────────────────────┤
│  Footer (20 bytes)                  │
│  indexOffset (8B) | numEntries (4B) │
│  bloomOffset (8B)                   │
└─────────────────────────────────────┘
```

Written directly to a `.tmp` file, then atomically renamed into place once complete — see
[docs/sstable.md](docs/sstable.md#atomic-install-atomic_installgo).

### Block Layout

```
┌────────────────────────────────────────────────────────────┐
│ numEntries (4 bytes)                                       │
│ entry 1: keyLen(4) | valLen(4) | internal key | value      │
│ entry 2: ...                                               │
│ ...                                                        │
│ CRC32 checksum (4 bytes)                                   │
└────────────────────────────────────────────────────────────┘
```

`internal key` = user key + an 8-byte trailer packing a sequence number and a kind (put/delete) —
there is no separate type byte; put vs. delete lives entirely in that trailer. Each block is
~4KB — aligned with OS page size. Small enough for I/O efficiency, large enough to amortize
index overhead.

### WAL Entry Layout

```
┌────────────────────────────────────────────────────────────────┐
│ totalLength(4B) | keyLen(4B) | valLen(4B) | internal key | val | CRC32(4) │
└────────────────────────────────────────────────────────────────┘
```

CRC32 on every WAL entry. A corrupted tail (incomplete write on crash)
is truncated cleanly on replay — the DB opens successfully with the last
clean state intact. The key is the same encoded internal key stored everywhere else, so replay
restores the original sequence numbers instead of reassigning new ones.

Exact byte layouts, field sizes, and the reasoning behind each design choice are in
[docs/sstable.md](docs/sstable.md) and [docs/wal.md](docs/wal.md).

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

### Why MVCC via internal-key sequence numbers, not copy-on-write snapshots

Every write is stamped with a monotonically increasing sequence number and stored as a distinct
internal key rather than overwriting the previous version in place (`internal/base`). A snapshot
is then just a saved sequence number: `GetSnapshot` pins one, and any `GetAt`/`ScanAt` call at
that number sees exactly the versions that existed at that instant, no matter what's written
afterward. The alternative — copying or versioning entire data structures per snapshot — would be
far more expensive; this design gets snapshot isolation almost for free, at the cost that a long-
lived snapshot keeps old versions alive until compaction can prove they're no longer needed (see
[docs/db.md](docs/db.md) and [docs/compaction.md](docs/compaction.md)).

---

## Benchmarks

Run on: Intel Core Ultra 7 155H, Linux, amd64 — real disk (not tmpfs), 2026-09-08
Command: `go test ./bench/ -bench=. -benchtime=3s -benchmem`

### Core Operations

| Benchmark                    | ops/sec    | ns/op   | allocs/op | Notes                                          |
| ----------------------------- | ---------- | ------- | --------- | ----------------------------------------------- |
| PutSequential                 | 27,153     | 36,827  | 14        | WAL append + memtable insert, real-disk fsync   |
| PutRandom                     | 26,419     | 37,851  | 14        | Memtable absorbs random write order             |
| GetMemtableHit                | 4,787,930  | 208.9   | 2         | No disk I/O — pure BTree lookup                 |
| GetSSTableHit                 | 1,786,710  | 559.8   | 4         | Block-cache hit — see [docs/cache.md](docs/cache.md) for cold-vs-warm |
| GetMiss (key absent)          | 12,300,123 | 81.3    | 2         | Bloom reject path for most misses               |
| Delete                        | 27,250     | 36,697  | 10        | Tombstone write — same fsync cost as Put        |
| Mixed (80% write / 20% read)  | 34,041     | 29,376  | 9         | Realistic workload approximation                |
| CompactionThroughput          | 20,443     | 48,916  | 94        | Flush + compaction every 500 ops                |

### Miss Latency vs SSTable Count

Isolates how read-miss cost grows as SSTable count increases — stays flat because per-SSTable
Bloom filters reject most misses before any block read, regardless of how many tables exist.

| SSTable Count | ns/op | allocs/op |
| ------------- | ----- | --------- |
| 1              | 87.7  | 2         |
| 5              | 114.2 | 2         |
| 10             | 88.9  | 2         |
| 20             | 116.8 | 2         |

Miss cost stays in the ~0.09–0.12 µs range from 1 to 20 tables — no linear growth with file
count. See [docs/bloomfilter.md](docs/bloomfilter.md) for why.

### Memtable vs SSTable reads

`GetMemtableHit` (208.9 ns) vs `GetSSTableHit` (559.8 ns) is now roughly a 2.7× gap, not the
much larger gap an uncached disk read would show — the block cache closes most of the distance
for a repeated block. `GetSSTableHit` here specifically measures a warm-cache read; the cold-read
cost the cache exists to avoid is measured separately in [docs/cache.md](docs/cache.md)
(~21,000 ns/op uncached vs ~560 ns/op cached on the same benchmark).

### Write path breakdown

| Benchmark      | What it isolates                            | ns/op | allocs/op |
| -------------- | -------------------------------------------- | ----- | --------- |
| WalAppend      | `wal.Append` — encode + buffered write       | 174.1 | 7         |
| MemtablePut    | `memtable.Put` — BTree insert                | 575.1 | 5         |
| PutSequential  | full `db.Put` path (WAL + memtable + flush)  | 36,827| 14        |

WAL + memtable together account for 12 of `PutSequential`'s 14 allocations — close to the whole
figure, unlike allocation counts, which aren't the story here. The gap between the two isn't
allocations, it's time: `WalAppend` + `MemtablePut` combined cost under 750 ns, but the full path
costs ~36,800 ns. On real disk, that gap is dominated by the periodic `fsync` inside
`wal.Sync()` (every `DEFAULT_SYNC_THRESHOLD` writes, see [docs/wal.md](docs/wal.md)) and,
whenever a flush is triggered, `sstable.Flush`'s own `fsync` plus its atomic install
(see [docs/sstable.md](docs/sstable.md)) — not allocation or GC pressure. `flushMemtableLocked`
still runs synchronously inside `Put`/`Delete` (see Known Limitations), so any write that crosses
the flush threshold pays that full cost before returning.

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

**Atomic install turns "crash mid-write" into a non-event, not a recovery scenario.**
Writing to a temp file and renaming into place means there's no window where a reader
could ever observe a half-written SSTable — the file either doesn't exist yet at its
final path, or it's complete. A directory fsync closes the subtler gap where the
rename itself hasn't reached disk yet, even though the syscall already returned
success. See [docs/faultinjection.md](docs/faultinjection.md) for the crash-window
test this made possible.

**A min-heap turns "merge N sorted things" into one small, reusable algorithm.**
Range scans, external merge sort, and every LSM engine's read path solve the same
problem: repeatedly pull the smallest available item from N sources, advance that
one source. `db.Scan`'s k-way merge is exactly that idea, applied to two source
types — memtable and SSTable — that otherwise know nothing about each other. See
[docs/db.md](docs/db.md).

**A cache doesn't need to be clever to help enormously.**
The block cache is a plain LRU — hashmap plus doubly-linked list, no scan-resistance — sharded 16
ways with a per-shard mutex so concurrent readers on unrelated keys don't serialize on one lock.
It still cut a repeated block read from ~21,000ns to ~560ns (~37× faster, ~200× fewer
allocations). Most of the win in caching comes from not redoing work at all; a smarter eviction
policy is a later refinement, not a prerequisite. See [docs/cache.md](docs/cache.md).

**A sequence number turns "point in time" into a comparable value.**
Copy-on-write or full data-structure versioning would make snapshots expensive to take. Instead,
every write gets a monotonically increasing sequence number and its own internal key rather than
overwriting the previous version, so a "snapshot" is just a saved integer — pin it
(`GetSnapshot`), read through it (`GetAt`/`ScanAt`), release it (`ReleaseSnapshot`) once done. The
same integer, tracked as a low watermark across every active snapshot, is what tells compaction
which old versions are still off-limits to reclaim. See [docs/db.md](docs/db.md) and
[docs/compaction.md](docs/compaction.md).

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
Compaction runs synchronously inside `flushMemtableLocked`. Under heavy writes this
inflates tail latency. Background compaction with throttling when debt builds is
the usual next step.

**4. High allocation count on a cold block decode**
A shared, sharded LRU block cache (`cache/`, see [docs/cache.md](docs/cache.md)) now caches
decoded blocks by `(FilePath, offset)`, so a *repeated* read of the same block drops from ~798
allocs/op to ~4 (measured directly: `BenchmarkGetSSTableHit` before/after wiring — ~21,000ns/op →
~560ns/op, ~37× faster). The first, cold read of a block still pays the full decode cost — a
buffer pool or arena would still help there, and hasn't been done. The cache is scan-resistant in
neither the old unsharded form nor the current sharded one (a large sequential scan can still
evict a shard's whole hot working set) — see the cache doc's known limitations.

**5. Memtable writes serialize on a single mutex**
`memtable.Put`/`Get`/`Delete` all take the same `sync.RWMutex` guarding the whole
BTree. Every concurrent writer queues behind one lock regardless of core count.
Confirmed via `BenchmarkMemtablePutParallel` (`bench/`, `b.RunParallel` across 22
goroutines): parallel writes are *slower* than sequential (654.1 ns/op vs 575.1
ns/op for the same op), not just non-improving — contention overhead (goroutines
blocking/waking on the lock) outweighs any benefit, since the actual tree insert
still only ever happens one goroutine at a time regardless of core count. The
standard answer is a lock-free skiplist (atomic CAS per node, arena-allocated) so
concurrent writers make real progress instead of taking turns.

**6. Synchronous flush blocks the writer**
`flushMemtableLocked` runs inline inside `Put`/`Delete` when the size threshold is crossed (both
now check it, via a shared `flushIfFullLocked`), so that call pays the full flush cost before
returning (see Write path breakdown above). A production engine rotates in a fresh memtable
immediately and flushes the full one on a background goroutine so writes never stall on it.

**7. Range scans aren't safe against concurrent compaction deleting files**
`db.Scan`/`ScanAt` (see [docs/db.md](docs/db.md)) return a `MergeIterator` that only holds
`db.mu` long enough to build its initial heap — each `Next()` call afterward runs unlocked. Its
underlying `sstable.Iterator`s hold a `FilePath` and reopen it per block; if `compaction.Compact`
deletes that file mid-scan, the scan's next read fails. MVCC snapshots narrow this gap on the
*data* side (a pinned snapshot keeps compaction from dropping versions it needs) but not on the
*file-handle* side — nothing pins an open file against concurrent deletion, confirmed still true
in both `sstable/iterator.go` and `compaction/compact.go`. Correct for the single-threaded case;
not for a scan running alongside a flush.

**8. `server`'s `SCAN` is not real Redis `SCAN`**
It's a direct wrapper over `db.Scan(lowerBound, upperBound)` — two explicit bound arguments, one
call returns every matching pair — not the cursor-based, bounded-batch incremental keyspace
iteration real Redis implements. A client relying on real `SCAN` semantics (a cursor argument,
`COUNT`, resuming a partial iteration) will not get them here. See [docs/server.md](docs/server.md).

---

## What's Next

Rough priority order for hardening and extending HCDB:

1. **Bloom filter tuning + measurement** — configurable false-positive targets and explicit miss-latency impact benchmarks
2. **Manifest / versioned metadata** — atomic *compaction*, O(1) sequence-number recovery on
   restart (`db.Open` currently re-derives it by scanning every SSTable, see
   [docs/db.md](docs/db.md)), and clearer crash recovery generally. Partially addressed:
   individual SSTable writes are now atomic (temp file + rename + directory fsync, see
   [docs/sstable.md](docs/sstable.md)) and a specific crash window (flush-then-WAL-reset) is
   tested and proven safe (see [docs/faultinjection.md](docs/faultinjection.md)) — but there is
   still no manifest, and compaction across multiple groups still isn't atomic as a whole.
3. **Streaming k-way merge for compaction** — bounded-memory compaction for large SSTables. Note:
   `db.Scan` already implements a streaming k-way merge (min-heap over memtable + SSTable
   iterators, see [docs/db.md](docs/db.md)) for *reads*; compaction's `BlockIterator` still loads
   whole tables into RAM and hasn't been switched over to it.
4. **Leveled (or hybrid) compaction** — stronger bounds on read amplification vs today’s size-tiered baseline
5. **Background compaction** — decouple flush latency from merge work; throttle when debt grows
6. **Cache scan-resistance** — the block cache is now sharded and mutex-protected
   (`cache/`, see [docs/cache.md](docs/cache.md)) with a measured ~37× speedup on repeated block
   reads, but still has no defense against a sequential scan evicting a shard's whole hot working
   set — a CLOCK-Pro-style algorithm is the natural next step.
7. **File reference counting for range scans** — pin an SSTable's file open for as long as an
   in-progress `Iterator` holds it, closing the last gap noted in Known Limitations item 7, now
   that MVCC snapshots already protect the data those files hold.
8. **Real cursor-based `SCAN`** in `server/` — bounded-batch, resumable iteration instead of the
   current one-shot range scan (see Known Limitations item 8), plus authentication and
   transactions (`MULTI`/`EXEC`) if HCDB's RESP surface grows further.
