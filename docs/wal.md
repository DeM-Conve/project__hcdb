# WAL (Write-Ahead Log)

Package: `wal/` — files: `wal_types.go`, `open.go`, `append.go`, `operators.go`, `sync.go`, `replay.go`

## Why it exists

The memtable lives in RAM. If the process dies, everything not yet flushed to an SSTable is
gone. The WAL is the durability layer: **every mutation is written to the log file before it is
applied to the memtable**. On restart we replay the log and rebuild the exact memtable state.

Order matters and is enforced in `db.Put` / `db.Delete`:

```
wal.Put(k, v)      → if this fails, we return early and never touch the memtable
memtable.Put(k, v)
```

So the log can never be *behind* the memtable. It can be *ahead* (log has a record whose
memtable apply never happened because we crashed in between) — replay just re-applies it, which
is idempotent.

## Types

```go
type WAL struct {
    File       *os.File
    BufWriter  *bufio.Writer
    putCounter int          // writes since last fsync
}

type Entry struct {
    Key   []byte // encoded internal key; its trailer carries the kind
    Value []byte
}
```

`Entry` no longer carries a `Type` byte. `Key` is an already-**encoded internal key** — the user
key plus an 8-byte trailer packing a sequence number and a kind (`base.InternalKeyKindSet` /
`base.InternalKeyKindDelete`, see [internal key format](#internal-keys-mvcc) below and
`internal/base/internal.go`). Put and Delete are no longer distinguished by a separate field
anywhere in the record — the trailer already says which one this is, so the record can't
disagree with itself.

`Open` opens the file with `O_CREATE|O_RDWR|O_APPEND` (0644) and wraps it in a `bufio.Writer`.
Append-only is a deliberate choice — no seeking, no rewriting, purely sequential disk writes.

## On-disk record format

```
[ totalLen (4B) ][ keyLen (4B) ][ valLen (4B) ][ internal key ][ value ][ crc32 (4B) ]
                 └───────────────────── dataBytes ─────────────────────┘
                 └────────── covered by the CRC ─────────────┘
```

All integers are **little-endian**. `keyLen` is the length of the *encoded internal key*
(user key + 8-byte trailer), not the raw user key.

- `totalLen = len(dataBytes) + 4` — the payload plus the trailing CRC, but **not** the 4 bytes
  of `totalLen` itself. Replay reads 4 bytes, then reads exactly `totalLen` more bytes.
- `crc32` is `crc32.ChecksumIEEE(dataBytes)` — IEEE polynomial, computed over the lengths, key
  and value.

`Append` builds `dataBytes` in an in-memory `bytes.Buffer` first (`writeDataBuff`), because we
need the full payload before we can compute its checksum and its length.

## Internal keys: MVCC

Every user key gets rewritten as an `internal key` before it reaches the WAL, the memtable or an
SSTable: `db.encodeNextKey` assigns the next sequence number (`db.seqNum`, a monotonically
increasing `atomic.Uint64` guarded in practice by `db.mu`) and packs `(seqNum, kind)` into an
8-byte trailer appended to the user key (`base.MakeInternalKey`/`InternalKey.Encode`, see
`internal/base/internal.go`). Two consequences that ripple through this package:

- A repeated write to the same user key produces a **distinct** internal key each time, rather
  than overwriting a prior WAL/memtable/SSTable entry in place — that's what makes it possible
  for a `db.GetSnapshot` reader to still see an old version after a newer write has landed.
- Replay preserves the original sequence numbers by decoding them straight out of the stored key
  (`base.DecodeInternalKey(e.Key).SeqNum()`) rather than assigning new ones — otherwise a restart
  would silently renumber every record and break ordering against SSTables written before the
  crash.

## Write path — `Put` / `Delete`

`operators.go` is the caller-facing API. Both funnel into the same `write` helper — the only
difference is the value:

- `Put(key, value)` → `Entry{Key: key, Value: value}`
- `Delete(key)` → `Entry{Key: key, Value: []byte{}}` — a **tombstone**. Deletes are
  writes, not removals. Nothing is ever erased in place. Both `key` arguments here are already
  encoded internal keys built by `db.encodeNextKey`, with the kind already baked into the
  trailer — `wal.Put` and `wal.Delete` are otherwise identical.

### Group commit / sync policy

Both increment `putCounter` and call `Sync()` every `DEFAULT_SYNC_THRESHOLD` (= 10) writes,
then reset the counter.

```go
walObj.putCounter++
if walObj.putCounter >= config.DEFAULT_SYNC_THRESHOLD {
    walObj.Sync()
    walObj.putCounter = 0
}
```

`Sync()` = `BufWriter.Flush()` (userspace buffer → kernel page cache) **then** `File.Sync()`
(page cache → physical disk, an actual `fsync`).

This is the durability/throughput knob. `fsync` is the single most expensive operation in the
whole write path, so we amortise it over 10 writes. **The trade-off is explicit: a crash can
lose up to the last 9 writes.** Set `DEFAULT_SYNC_THRESHOLD = 1` for full durability at a large
throughput cost.

## Recovery path — `Replay`

Called once by `db.Open` → `rebuildMemtable`. It flushes any pending buffered writes, seeks to
offset 0, and reads records in a loop until EOF.

A `countingReader` wraps the file purely to track the byte offset (`cr.pos`), because we need
`startOffset` — the offset of the record we're currently reading — to be able to truncate at a
clean boundary.

For each record it validates, in order:

1. Read `totalLen`. Clean `io.EOF` here → normal end of log, stop.
2. `totalLen` sanity bound: `4 + maxEncodedKeyLen + 4 + MAX_VALUE_LENGTH + 4`, where
   `maxEncodedKeyLen = MAX_KEY_LENGTH + base.InternalTrailerLen` — the encoded key's 8-byte
   trailer has to be accounted for on top of the user-key bound, or a legitimately maximum-sized
   key would be mistaken for corruption. Guards against a garbage length causing a huge
   allocation.
3. `io.ReadFull` of exactly `totalLen` bytes — a short read means the record was torn.
4. `len(recordBuf) >= 4` so the CRC slice is valid.
5. **CRC check**: recompute over `recordBuf[:len-4]` and compare with the stored trailing 4
   bytes. Mismatch → corruption.
6. Parse `keyLen`, `valLen` from the payload.
7. `keyLen <= maxEncodedKeyLen && valLen <= MAX_VALUE_LENGTH`.
8. `io.ReadFull` for the key, then the value.

**Any failure at any of these steps does the same thing:**

```go
walObj.File.Truncate(startOffset)
break
```

That is the core recovery rule: *the log is valid up to the first bad record; everything from
there on is discarded.* This is exactly the right behaviour for an append-only log — a partial
write can only ever be at the tail (the process died mid-`write`), so truncating to the last
known-good boundary leaves a clean file we can keep appending to.

Entries that survive are returned in write order and replayed by `db.rebuildMemtable`, which
decodes each entry's key (`base.DecodeInternalKey`) and switches on its `Kind()` to call
`mem.Put` / `mem.Delete` — and tracks the highest sequence number it sees, so `db.seqNum` resumes
above every record already durable in the log (see [db.md](db.md#opendb)).

## WAL reset on flush

`db.resetWAL()` (in `db/db.go`) runs after a successful memtable flush:

```go
db.wal.File.Truncate(0)
db.wal.File.Seek(0, 0)
db.wal.BufWriter.Reset(db.wal.File)
```

Once the memtable's contents are durable in an SSTable, the log records that produced it are
redundant, so the log is emptied and reused. This is what keeps the WAL bounded — it never
grows past one memtable's worth of writes. `BufWriter.Reset` is required to drop any buffered
bytes that would otherwise be written after the truncate.

## Known limitations

- **`O_APPEND` + `Truncate`/`Seek`.** The file is opened with `O_APPEND`, which means the
  kernel forces every write to the end of file regardless of the file offset. The `Seek(0,0)`
  calls in `Replay` and `resetWAL` therefore only affect *reads*. It works, but it's subtle.
- **Single WAL, single memtable.** There is no "old WAL kept alive while the old memtable is
  being flushed" — flush is synchronous, so it doesn't need one.
- **No manifest / log numbering.** One file, path from `Config.WALPath`.
- **Not concurrency-safe on its own.** `putCounter` and the `bufio.Writer` have no internal
  locking. This package relies entirely on its caller for safety: `db.Put`/`db.Delete` now hold
  `db.mu` for the whole call (including the WAL write), which is what actually prevents
  concurrent writers from interleaving mid-record — see [db.md](db.md#concurrency). A caller
  that used this package directly, without that external lock, would race.

## Related

- [memtable.md](memtable.md) — what replay rebuilds
- [db.md](db.md) — where the write ordering, locking and `resetWAL` live
- [config.md](config.md) — `DEFAULT_SYNC_THRESHOLD`, `MAX_KEY_LENGTH`, `MAX_VALUE_LENGTH`
