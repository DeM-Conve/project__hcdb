# Developer Guides

Internal notes on how hcdb is put together. For what the project *is*, see the
[root README](../README.md).

## Layout

```
hcdb/
├── db/               orchestration + public API + range-scan merge iterator
├── wal/              write-ahead log
├── memtable/         in-memory write buffer
├── sstable/          on-disk immutable files
├── bloomfilter/      membership filter for sstable
├── compaction/       background file merging
├── cache/            shared LRU block cache
├── faultinjection/   torn-write simulation for crash-recovery tests
├── config/           shared constants
├── bench/            benchmarks
└── docs/             these notes
```

One package per responsibility. Files inside a package are split by concern rather than
grouped into one big file — types in their own file, each operation roughly in its own file.

## How the packages relate

```
                  db
                  │  (knows about everything)
    ┌─────┬───────┼──────────┬────────────┬──────────┐
   wal  memtable  sstable  compaction   cache         │
                    │  │       │          │           │
              bloomfilter cache┘          │           │
                    │      │              │           │
                  config ◄─┴──────────────┴───────────┘
                  (imports nothing)
```

The important structural rule: **only `db` knows about more than one sibling.** `wal`,
`memtable` and `sstable` are unaware of each other. `compaction` depends on `sstable`, and
`sstable` on `bloomfilter` and now `cache` — all one-directional. `config` sits at the bottom and
imports nothing, which is what keeps the graph acyclic.

`cache` is a leaf like `config` — it imports nothing from hcdb — but unlike `config` it's not
depended on by everyone, only by `sstable` (which holds the cache reference and calls it from
`readBlock`) and `db` (which owns the one shared instance and constructs it in `Open`).

`faultinjection` doesn't appear in this graph at all — it's a test-only dependency, imported by
`wal`'s and `db`'s test files, never by production code.

That means a component can be reasoned about, tested, or replaced on its own, and the rules
that make the storage engine *correct* live in exactly one place: `db`.

## Where data lives

Three tiers, each in its own package:

| Tier | Package | Lifetime |
|---|---|---|
| Durable log | `wal` | until the memtable it describes is flushed |
| In-memory buffer | `memtable` | until it reaches its size limit |
| On-disk files | `sstable` | immutable; deleted only by `compaction` |

Writes move down the tiers; reads walk them newest-first.

## The guides

| Doc | Package |
|---|---|
| [db.md](db.md) | `db/` |
| [wal.md](wal.md) | `wal/` |
| [memtable.md](memtable.md) | `memtable/` |
| [sstable.md](sstable.md) | `sstable/` |
| [bloomfilter.md](bloomfilter.md) | `bloomfilter/` |
| [compaction.md](compaction.md) | `compaction/` |
| [cache.md](cache.md) | `cache/` |
| [faultinjection.md](faultinjection.md) | `faultinjection/` (test-only) |
| [config.md](config.md) | `config/` |

Read `config` first for the vocabulary, then follow the write path (`wal` → `memtable` →
`sstable`), then `compaction`, then `cache`, then `db` (which covers both `Get`'s read path and
`Scan`'s range-scan merge) to see how it all connects. `faultinjection.md` is the odd one out —
it documents test infrastructure, not a runtime package, but explains real production code it
led to (`sstable`'s atomic install). Each guide ends with its own known-limitations section.
