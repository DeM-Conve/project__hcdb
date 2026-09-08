# Config

Package: `config/` — file: `config.go`

## Why it exists

Every tunable number and shared constant lives here. Two reasons it's its own package:

1. **No magic numbers scattered through the code.** `4096` in the SSTable writer means nothing;
   `config.DEFAULT_BLOCK_SIZE` means something.
2. **It's the dependency sink.** `config` imports nothing. Every other package imports it. That
   keeps the dependency graph acyclic — `wal` and `sstable` both need `OP_PUT`/`OP_DELETE`, and
   without a shared leaf package one would have to import the other.

```
config  ←── wal, memtable, bloomfilter, sstable, compaction, db
  (imports nothing)
```

## Operation types

```go
OP_PUT    = uint8(0)
OP_DELETE = uint8(1)
```

The same two values are used for the `Type` field in **four** different structs across three
packages — `wal.Entry`, `memtable.Item`, `sstable.BlockEntry`, and the type byte in both on-disk
formats. That's what lets a record flow from a WAL append, through the memtable, into an SSTable
block and out through compaction without ever being translated.

`OP_DELETE` is the tombstone marker. There is no "remove" anywhere in the system — a delete is a
write of a record whose type is `OP_DELETE` and whose value is empty.

> Note: `memtable.Put` and `memtable.Delete` write the literal values `0` and `1` rather than
> the named constants, though `memtable.Get` compares against `config.OP_DELETE`. Same values,
> just inconsistent.

## Validation bounds

```go
MAX_KEY_LENGTH   = 1024        // 1 KB
MAX_VALUE_LENGTH = 1048576     // 1 MB
```

These are **not enforced on write** — nothing rejects an oversized key at `Put` time. They're
used exclusively in `wal.Replay` as sanity checks while parsing a possibly-corrupt log:

- The `totalLen` bound: `1 + 4 + MAX_KEY_LENGTH + 4 + MAX_VALUE_LENGTH + 4` — a garbage length
  field would otherwise cause a multi-gigabyte allocation.
- The per-field check on `keyLength` / `valueLength` after parsing.

Either failing triggers truncate-to-last-good-record. So today they're really "how large a
record can be before replay treats the log as corrupt" — which means writing a value larger than
1 MB would make it un-replayable. Worth enforcing at the write path.

## Memtable

```go
DEFAULT_BTREE_DEGREE        = 32
DEFAULT_MEMTABLE_FLUSH_SIZE = 4 * 1024 * 1024   // 4MB
```

**`DEFAULT_BTREE_DEGREE = 32`** — each B-tree node holds up to 63 items (2×32−1). Higher degree
means shallower trees and better cache locality per node, but more copying on insert. 32 is a
reasonable middle.

**`DEFAULT_MEMTABLE_FLUSH_SIZE = 4MB`** — the central write-path knob, checked in `db.Put`
against `memtable.Size()`. It controls a genuine trade-off:

- *Larger*: fewer SSTables, less compaction work, better write throughput — but more RAM, longer
  WAL replay on restart, and a longer stall when the flush finally happens.
- *Smaller*: more, smaller SSTables → more compaction and worse read amplification.

Remember `memtable.Size()` counts payload bytes only (no B-tree overhead, no per-entry framing),
so actual memory at flush time exceeds 4 MB and so does the resulting file.

## SSTable

```go
DEFAULT_BLOCK_SIZE = 4096      // 4KB
```

The unit of disk I/O. 4 KB matches a typical filesystem page, so a block read is usually one
page-cache hit. It's the granularity of everything in the read path: a lookup reads exactly one
block, and the sparse index holds exactly one entry per block.

- *Larger blocks*: smaller index (less RAM), better sequential throughput — but each lookup
  reads and decodes more, and the in-block linear scan gets longer.
- *Smaller blocks*: more precise reads — but a bigger index and more per-block overhead
  (4-byte header + 4-byte CRC each).

It's a soft target, checked after each entry is added, so blocks overshoot slightly.

## WAL

```go
DEFAULT_SYNC_THRESHOLD = 10
```

`fsync` every 10 writes (`wal.Put` / `wal.Delete` count into `putCounter`). This is the
**durability vs. throughput** dial, and it's the most consequential constant in the codebase:

- `= 1` → every write is durable on return; correct, and roughly an order of magnitude slower.
- `= 10` → up to 9 acknowledged writes can be lost in a crash.
- Larger → faster still, proportionally more data at risk.

## Compaction

```go
DEFAULT_COMPACTION_THRESHOLD = 4    // trigger compaction when this many SSTables exist
DEFAULT_SIMILAR_SIZE_RATIO   = 2    // two tables are "similar size" if larger/smaller <= this
```

**`THRESHOLD = 4`** — `Compact` returns immediately below this count. Low enough to keep read
amplification down, high enough that we don't merge on every single flush.

**`SIMILAR_SIZE_RATIO = 2`** — the size-tiering width. Note `isSimilarSize` uses **integer**
division, so the real cutoff is under 3× (a 2.9 ratio truncates to 2 and passes). A wider ratio
merges more aggressively (fewer files, more write amplification); narrower merges only
near-identical tables.

## Bloom filter

```go
DEFAULT_BLOOM_FALSE_POSITIVE_RATE = 0.01     // 1% false positive rate
DEFAULT_BLOOM_EXPECTED_KEYS       = 100000   // expected keys per SSTable
```

These feed `findOptimalParamsValues(n, p)`, giving `m ≈ 958 506` bits (~117 KB on disk, ~958 KB
in RAM as a `[]bool`) and `k ≈ 7` hashes.

The false-positive rate trades memory for wasted disk reads: 1% of negative lookups pay for a
block read that finds nothing. Dropping to 0.001 costs ~50% more bits for 10× fewer wasted reads.

**`EXPECTED_KEYS` is applied uniformly to every SSTable regardless of its actual size**, which
is the main weakness — small tables waste memory, tables above 100 000 keys silently exceed the
1% target rate.

## Cache

```go
DEFAULT_BLOCK_CACHE_ENTRIES = 256
```

Number of decoded blocks the shared `cache.LRU` holds (see [cache.md](cache.md)) — entries, not
bytes. At `DEFAULT_BLOCK_SIZE` (4 KB) that's roughly 1 MB of decoded blocks. An unvalidated
starting guess, not tuned against a real hit-rate measurement yet.

## `Config` struct

```go
type Config struct {
    WALPath string
    SSTDir  string
}

func DefaultConfig(walPath, sstDir string) *Config { ... }
```

Everything above is a compile-time constant; only the two paths are runtime-configurable. So
tuning any behaviour currently requires a recompile — moving these constants into `Config`
fields (with the constants as defaults) would be the natural next step.

Note `db.Open` takes `config.Config` **by value** while `DefaultConfig` returns a `*Config`, so
callers dereference at the call site.

## Quick reference

| Constant | Value | Owner | What it controls |
|---|---|---|---|
| `OP_PUT` / `OP_DELETE` | 0 / 1 | all | record type, tombstone marker |
| `MAX_KEY_LENGTH` | 1 KB | wal | replay sanity bound |
| `MAX_VALUE_LENGTH` | 1 MB | wal | replay sanity bound |
| `DEFAULT_BTREE_DEGREE` | 32 | memtable | B-tree fanout |
| `DEFAULT_MEMTABLE_FLUSH_SIZE` | 4 MB | db | when to flush to disk |
| `DEFAULT_BLOCK_SIZE` | 4 KB | sstable | disk I/O unit, index granularity |
| `DEFAULT_SYNC_THRESHOLD` | 10 | wal | writes per fsync (durability window) |
| `DEFAULT_COMPACTION_THRESHOLD` | 4 | compaction | min tables before merging |
| `DEFAULT_SIMILAR_SIZE_RATIO` | 2 | compaction | size-tier width |
| `DEFAULT_BLOOM_FALSE_POSITIVE_RATE` | 0.01 | bloomfilter | filter accuracy vs. size |
| `DEFAULT_BLOOM_EXPECTED_KEYS` | 100 000 | bloomfilter | filter sizing per SSTable |
| `DEFAULT_BLOCK_CACHE_ENTRIES` | 256 | cache | shared decoded-block cache size |

## Related

- [db.md](db.md) · [wal.md](wal.md) · [memtable.md](memtable.md) · [sstable.md](sstable.md) ·
  [compaction.md](compaction.md) · [bloomfilter.md](bloomfilter.md) · [cache.md](cache.md)
