# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-09-29

First tagged release.

### Added
- Write-ahead log with `fsync`-before-ack durability and torn-write recovery.
- B-tree memtable with tombstones and internal keys (sequence number + op type).
- Block-based SSTables with atomic install (temp file + rename + directory fsync).
- Bloom filters for fast key-miss rejection.
- Size-tiered compaction.
- Sharded LRU block cache.
- MVCC snapshot reads via sequence-number watermarks (`GetSnapshot`, `GetAt`, `ScanAt`).
- RESP wire-protocol server (`hcdb-server`), usable from `redis-cli` and Redis clients.
- Fault-injection test suite for crash recovery.
- Dockerfile and docker-compose setup.

[Unreleased]: https://github.com/DeM-Conve/project__hcdb/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/DeM-Conve/project__hcdb/releases/tag/v1.0.0
