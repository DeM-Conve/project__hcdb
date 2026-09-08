# Fault injection — crash & durability testing

Package: `faultinjection/` — file: `faulty_writer.go`

Related production code this exists to test: `sstable/atomic_install.go`,
`db/db_test.go`, `wal/replay_test.go`.

## Why it exists

You can't unit-test "what happens if the process dies mid-write" by actually killing the
process. `FaultyWriter` fakes the failure instead: it wraps any `io.Writer` and, past a
configured byte count, starts returning errors — including **partial success followed by an
error**, not just a clean all-or-nothing failure. That partial-write behavior matters: a real
crash mid-`write()` can leave some bytes on disk and the rest lost, which is a torn write, not a
clean failure. Modeled on Pebble's `vfs/errorfs`.

## Type

```go
type FaultyWriter struct {
    w         io.Writer
    written   int
    FailAfter int
}
```

`Write(p []byte)`:

- If `written >= FailAfter` already: fail immediately, write nothing.
- If `p` fits entirely within the remaining allowance: pass it straight through.
- Otherwise: write only the bytes that fit (`p[:allowed]`), **then still return an error** even
  though some bytes really were written. This is what makes it simulate a torn write instead of
  an atomic failure — the caller sees `(n > 0, err != nil)`, exactly what a real partial `write()`
  syscall followed by a crash would produce.

## What it proved: `wal.TestTornWriteRecovery` (`wal/replay_test.go`)

Builds a `wal.WAL` manually with `BufWriter: bufio.NewWriter(faulty)`, writes 3 good entries and
flushes cleanly, then writes a 4th entry that tears at 9 bytes on flush. Reopens the file fresh
and calls `Replay()`. Confirms exactly 3 correct entries recover and the file is truncated to
111 bytes — i.e. `wal.Replay`'s truncate-at-first-bad-record logic (see [wal.md](wal.md))
actually survives a real torn write, not just a clean EOF.

## What it led to: atomic SSTable install (`sstable/atomic_install.go`)

Reasoning about torn writes on the WAL raised the same question for SSTable files: what if a
crash happens *while* an SSTable is being written? Before this, `sstable.Flush` wrote directly
to `<UnixNano>.sst` — a crash mid-write would leave a corrupt, partially-written file at the
final path, indistinguishable from a real one.

```go
func atomicInstall(tmpPath, finalPath string) error {
    os.Rename(tmpPath, finalPath)   // atomic at the filesystem level
    dir, _ := os.Open(filepath.Dir(finalPath))
    defer dir.Close()
    return dir.Sync()               // fsync the directory entry itself
}
```

Both `sstable.Flush` and `WriteSSTableFromBlockEntries` now write to `<finalPath>.tmp`, `fsync`
the file, then call `atomicInstall`. `os.Rename` is atomic — a crash before it leaves only a
`.tmp` file (never observed at `finalPath`); a crash after it leaves the fully-written file at
`finalPath`. There is no state where `finalPath` exists but is incomplete.

The directory `fsync` is the less obvious half: `rename()` returning success only guarantees the
directory *entry* was updated in the kernel's page cache — a crash before that entry itself is
flushed to disk can still lose the rename, even though the syscall already returned. Both write
functions also `defer` an `os.Remove(tmpPath)` on any error path, so a failed flush doesn't leave
an orphaned `.tmp` file behind.

## What it proved: `db.TestCrashBetweenFlushAndWALReset` (`db/db_test.go`)

`db.flushMemtable` runs `sstable.Flush` (now atomic) then `resetWAL()`. This test simulates a
crash in the gap between those two steps: it calls `sstable.Flush` directly (bypassing
`flushMemtable`), deliberately skipping `resetWAL()`, then reopens a fresh `DB` over the same
paths — modeling a process restart.

Result: the WAL still has all 3 original puts (never truncated), *and* the SSTable directory has
the just-installed file. On reopen, WAL replay repopulates the memtable with the same 3 entries
the SSTable already has. Reads are still correct — redundant, not corrupting, because nothing
else can write between those two steps (`flushMemtable` holds `db.mu` for the whole call). The
test asserts both the read correctness *and* the redundancy explicitly (`recovered.memtable.Size()
!= 0` and `len(recovered.sstables) == 1`) — proving the mechanism, not just the outcome.

## Known limitations

- **No direct unit test of `FaultyWriter` itself** — only exercised indirectly via
  `TestTornWriteRecovery`. Acknowledged gap, not pursued.
- **`FailAfter` is a byte count, not a syscall count.** A real crash can happen at any point
  within a single large `Write` call too; this only models "fails after N bytes across however
  many `Write` calls it takes to reach that count."

## Related

- [wal.md](wal.md) — `Replay`'s truncate-on-corruption logic, the thing being tested
- [sstable.md](sstable.md) — atomic install, `Flush`, `WriteSSTableFromBlockEntries`
- [db.md](db.md) — `flushMemtable`'s crash window
