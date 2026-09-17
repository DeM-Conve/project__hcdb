# Developer Guides

Internal notes on how HCDB is put together. For what the project *is*, see the
[root README](../README.md).

## Layout

```
hcdb/
├── db/               orchestration + public API + snapshots + range-scan merge iterator
├── wal/              write-ahead log
├── memtable/         in-memory write buffer
├── sstable/          on-disk immutable files
├── bloomfilter/      membership filter for sstable
├── compaction/       size-tiered file merging, snapshot-aware
├── cache/            shared sharded LRU block cache
├── internal/base/    internal key format, MVCC sequence numbers, snapshot watermark
├── resp/             RESP wire protocol (parse + serialize)
├── server/           RESP-speaking TCP server on top of db
├── cmd/hcdb-server/  server entrypoint (env-configured)
├── faultinjection/   torn-write simulation for crash-recovery tests
├── config/           shared constants
├── bench/            benchmarks
├── Dockerfile, docker-compose.yml   container image + compose wiring
└── docs/             these notes
```

One package per responsibility. Files inside a package are split by concern rather than
grouped into one big file — types in their own file, each operation roughly in its own file.

## How the packages relate

```
                          db
                          │  (knows about everything below it)
    ┌──────┬──────────┬───┼──────────┬────────────┬──────────┐
   wal  memtable   sstable        compaction     cache        │
                     │   │            │            │          │
              bloomfilter cache┘      │            │          │
                     │      │         │            │          │
                   config ◄─┴─────────┴────────────┴──────────┘
                   (imports nothing)

   internal/base ◄── wal, memtable, sstable, compaction, db
   (internal key format, SeqNum, Watermark — imports nothing)

   server ──► db          (RESP command dispatch on top of the public API)
   resp   ──  (imported by server; knows nothing about db)
   cmd/hcdb-server ──► db, server, config
```

The important structural rule: **only `db` knows about more than one storage-engine sibling.**
`wal`, `memtable` and `sstable` are unaware of each other. `compaction` depends on `sstable`, and
`sstable` on `bloomfilter` and `cache` — all one-directional. `config` sits at the bottom and
imports nothing, which is what keeps the graph acyclic. `internal/base` is a second, parallel leaf
package: it defines the internal-key/sequence-number/kind format and the snapshot `Watermark`,
and every storage-engine package that needs to encode, decode or compare a key imports it —
but it, like `config`, imports nothing back.

`cache` is a leaf like `config` — it imports nothing from HCDB — but unlike `config` it's not
depended on by everyone, only by `sstable` (which holds the cache reference and calls it from
`readBlock`) and `db` (which owns the one shared instance and constructs it in `Open`).

`server` sits **above** `db`, not beside its other siblings — it's a second, independent way to
drive the same public API `main.go`'s demo uses, translating RESP commands into `db.DB` method
calls. `resp` is lower still: a standalone wire-format package with no knowledge of `db` or
`server`, imported only by `server`. `cmd/hcdb-server` is the thinnest layer of all — an
entrypoint that reads environment variables, opens a `db.DB`, and hands it to a `server.Server`.

`faultinjection` doesn't appear in this graph at all — it's a test-only dependency, imported by
`wal`'s and `db`'s test files, never by production code.

That means a component can be reasoned about, tested, or replaced on its own, and the rules
that make the database *correct* live in exactly one place: `db`.

## Where data lives

Three tiers, each in its own package:

| Tier | Package | Lifetime |
|---|---|---|
| Durable log | `wal` | until the memtable it describes is flushed |
| In-memory buffer | `memtable` | until it reaches its size limit |
| On-disk files | `sstable` | immutable; deleted only by `compaction` |

Writes move down the tiers; reads walk them newest-first. Every write is stamped with a sequence
number before it reaches any tier (see `internal/base`), which is what lets a `db.GetSnapshot`
reader see a consistent, older view of all three at once — see [db.md](db.md).

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
| [resp.md](resp.md) | `resp/` |
| [server.md](server.md) | `server/` |
| [deployment.md](deployment.md) | `cmd/hcdb-server/`, `Dockerfile`, `docker-compose.yml` |
| [faultinjection.md](faultinjection.md) | `faultinjection/` (test-only) |
| [config.md](config.md) | `config/` |

Read `config` first for the vocabulary, then follow the write path (`wal` → `memtable` →
`sstable`), then `compaction`, then `cache`, then `db` (which covers both the read path, MVCC
snapshots, and `Scan`'s range-scan merge) to see how it all connects. From there, `resp` →
`server` → `deployment` cover the network-facing layer built on top of `db`'s public API.
`faultinjection.md` is the odd one out — it documents test infrastructure, not a runtime package,
but explains real production code it led to (`sstable`'s atomic install). Each guide ends with
its own known-limitations section.
