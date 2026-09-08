package wal

import (
	"bufio"
	"os"
)

// ============================================================
// WAL Record Format (on disk, append-only):
//
//   +----------+--------+---------+---------+--------------+-------+---------+
//   | totalLen | type   | keyLen  | valLen  | internal key | value | crc32   |
//   | 4 bytes  | 1 byte | 4 bytes | 4 bytes | bytes        | bytes | 4 bytes |
//   +----------+--------+---------+---------+--------------+-------+---------+
//              └──────────────── covered by crc32 ─────────────────┘
//              └────────────────── totalLen counts this + crc32 ────────────┘
//
// All integers are little-endian.
//
// - totalLen excludes its own 4 bytes, so replay reads 4 bytes then exactly
//   totalLen more.
// - The key is an encoded internal key (user key + 8-byte trailer holding the
//   sequence number and kind), so replay restores the original sequence
//   numbers instead of assigning new ones. See internal/base.
// - A torn tail is truncated at the last good record on replay; see replay.go.
// ============================================================

type WAL struct {
	File       *os.File
	BufWriter  *bufio.Writer
	putCounter int
}

type Entry struct {
	Type  uint8
	Key   []byte
	Value []byte
}
