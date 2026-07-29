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
    Type  uint8   // config.OP_PUT (0) | config.OP_DELETE (1)
    Key   []byte
    Value []byte
}
```

`Open` opens the file with `O_CREATE|O_RDWR|O_APPEND` (0644) and wraps it in a `bufio.Writer`.
Append-only is a deliberate choice — no seeking, no rewriting, purely sequential disk writes.

## On-disk record format

```
[ totalLen (4B) ][ type (1B) ][ keyLen (4B) ][ valLen (4B) ][ key ][ value ][ crc32 (4B) ]
                 └──────────────────── dataBytes ──────────────────────────┘
                 └────────── covered by the CRC ─────────────┘
```

All integers are **little-endian**.

- `totalLen = len(dataBytes) + 4` — the payload plus the trailing CRC, but **not** the 4 bytes
  of `totalLen` itself. Replay reads 4 bytes, then reads exactly `totalLen` more bytes.
- `crc32` is `crc32.ChecksumIEEE(dataBytes)` — IEEE polynomial, computed over type + lengths +
  key + value.

`Append` builds `dataBytes` in an in-memory `bytes.Buffer` first (`writeDataBuff`), because we
need the full payload before we can compute its checksum and its length.

## Write path — `Put` / `Delete`

`operators.go` is the caller-facing API. Both do the same thing with a different op type:

- `Put(key, value)` → `Entry{Type: OP_PUT}`
- `Delete(key)` → `Entry{Type: OP_DELETE, Value: []byte{}}` — a **tombstone**. Deletes are
  writes, not removals. Nothing is ever erased in place.

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
2. `totalLen` sanity bound: `1 + 4 + MAX_KEY_LENGTH + 4 + MAX_VALUE_LENGTH + 4`. Guards
   against a garbage length causing a huge allocation.
3. `io.ReadFull` of exactly `totalLen` bytes — a short read means the record was torn.
4. `len(recordBuf) >= 4` so the CRC slice is valid.
5. **CRC check**: recompute over `recordBuf[:len-4]` and compare with the stored trailing 4
   bytes. Mismatch → corruption.
6. Parse type, `keyLen`, `valLen` from the payload.
7. `keyLen <= MAX_KEY_LENGTH && valLen <= MAX_VALUE_LENGTH`.
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
switches on `Type` and calls `mem.Put` / `mem.Delete`.

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
- **Not concurrency-safe on its own.** `putCounter` and the `bufio.Writer` are unguarded;
  `db.Put` does not hold `db.mu` while calling `wal.Put`. Concurrent writers would race.

## Related

- [memtable.md](memtable.md) — what replay rebuilds
- [db.md](db.md) — where the write ordering and `resetWAL` live
- [config.md](config.md) — `DEFAULT_SYNC_THRESHOLD`, `MAX_KEY_LENGTH`, `MAX_VALUE_LENGTH`
