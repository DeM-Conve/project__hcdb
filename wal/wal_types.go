package wal

import (
	"bufio"
	"os"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// ============================================================
// WAL Record Format (on disk, append-only):
//
//   +----------+---------+---------+--------------+-------+---------+
//   | totalLen | keyLen  | valLen  | internal key | value | crc32   |
//   | 4 bytes  | 4 bytes | 4 bytes | bytes        | bytes | 4 bytes |
//   +----------+---------+---------+--------------+-------+---------+
//              └─────────── covered by crc32 ──────────┘
//              └───────────── totalLen counts this + crc32 ───────┘
//
// All integers are little-endian.
//
// - totalLen excludes its own 4 bytes, so replay reads 4 bytes then exactly
//   totalLen more.
// - The key is an encoded internal key (user key + 8-byte trailer holding the
//   sequence number and kind), so replay restores the original sequence
//   numbers instead of assigning new ones. See internal/base.
// - There is no separate type byte: put vs delete lives in the key's trailer,
//   so the record cannot disagree with itself.
// - A torn tail is truncated at the last good record on replay; see replay.go.
// ============================================================

// maxEncodedKeyLen bounds a key as it is stored on disk. config.MAX_KEY_LENGTH
// limits the USER key; every stored key carries an 8-byte trailer on top of
// that, so replay must compare against the sum. Derived once here rather than
// added at each call site — the two checks in replay.go disagreeing is exactly
// how a valid max-sized key gets mistaken for corruption.
const maxEncodedKeyLen = config.MAX_KEY_LENGTH + base.InternalTrailerLen

type WAL struct {
	File       *os.File
	BufWriter  *bufio.Writer
	putCounter int
}

type Entry struct {
	Key   []byte // encoded internal key; its trailer carries the kind
	Value []byte
}
