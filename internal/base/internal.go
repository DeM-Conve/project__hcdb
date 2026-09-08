package base

import (
	"cmp"
	"encoding/binary"
)

// ============================================================
// Internal Key Format (as stored in the memtable, WAL and SSTables):
//
//   +--------------+--------------------------+
//   | user key     | trailer                  |
//   | N bytes      | 8 bytes, little-endian   |
//   +--------------+--------------------------+
//                    │
//                    └─ one uint64, split as:
//
//                       bits 63..8  sequence number (56 bits)
//                       bits  7..0  kind (1 byte: Set / Delete)
//
//   MakeTrailer packs it as (seqNum << 8) | kind, which is why the sequence
//   number is capped at 2^56-1 — 8 of the 64 bits belong to the kind.
//
// ------------------------------------------------------------
// Ordering (InternalCompare):
//
//   1. user key ascending
//   2. then trailer DESCENDING, so a higher sequence number sorts first
//
//   Comparing the packed trailer as one integer gives rule 2 and the kind
//   tiebreaker at once, since the sequence number occupies the high bits.
//
//   Example — the same four keys, sorted:
//
//     a@47   ← newest version of "a" comes first
//     a@9
//     b@200  ← "b" still sorts after "a", despite the far higher seqnum
//     b@15
//
//   Newest-wins therefore falls out of plain sorted order: a reader takes the
//   first entry it finds for a user key, with no special casing.
//
// ------------------------------------------------------------
// Search keys (MakeSearchKey):
//
//   A search key carries SeqNumMax, so it sorts BEFORE every real version of
//   the same user key — seek to it and the next entry is the newest version.
//   Callers must not treat "sorts before everything" as "absent".
// ============================================================

// SeqNum is a sequence number defining precedence among identical keys. A key
// with a higher sequence number takes precedence over a key with an equal user
// key of a lower sequence number. Sequence numbers are stored durably within
// the internal key "trailer" as a 7-byte (uint56) uint, and the maximum
// sequence number is 2^56-1.
type SeqNum uint64

// SeqNumMax is the largest valid sequence number. Reserved as a sentinel for
// search keys, so no real record is ever assigned it.
const SeqNumMax SeqNum = 1<<56 - 1

type InternalKeyKind uint8

// These constants are part of the file format, and should not be changed.
// NOTE: these follow Pebble's values, which are the inverse of hcdb's
// config.OP_PUT / config.OP_DELETE.
const (
	InternalKeyKindDelete InternalKeyKind = 0
	InternalKeyKindSet    InternalKeyKind = 1

	InternalKeyKindMax     InternalKeyKind = 1
	InternalKeyKindInvalid InternalKeyKind = 255
)

// Compare returns -1, 0, or +1 depending on whether a is less than, equal to,
// or greater than b.
type Compare func(a, b []byte) int

type InternalKeyTrailer uint64

// MakeTrailer constructs an internal key trailer from the specified sequence
// number and kind.
func MakeTrailer(seqNum SeqNum, kind InternalKeyKind) InternalKeyTrailer {
	return (InternalKeyTrailer(seqNum) << 8) | InternalKeyTrailer(kind)
}

// SeqNum returns the sequence number component of the trailer.
func (t InternalKeyTrailer) SeqNum() SeqNum {
	return SeqNum(t >> 8)
}

// Kind returns the key kind component of the trailer.
func (t InternalKeyTrailer) Kind() InternalKeyKind {
	return InternalKeyKind(t & 0xff)
}

// InternalKey is a key used for the in-memory and on-disk partial DBs that
// make up a pebble DB.
type InternalKey struct {
	UserKey []byte
	Trailer InternalKeyTrailer
}

// InternalTrailerLen is the number of bytes used to encode InternalKey.Trailer.
const InternalTrailerLen = 8

// MakeInternalKey constructs an internal key from a specified user key,
// sequence number and kind.
func MakeInternalKey(userKey []byte, seqNum SeqNum, kind InternalKeyKind) InternalKey {
	return InternalKey{
		UserKey: userKey,
		Trailer: MakeTrailer(seqNum, kind),
	}
}

// MakeSearchKey constructs an internal key that is appropriate for searching
// for a the specified user key. The search key contain the maximal sequence
// number and kind ensuring that it sorts before any other internal keys for
// the same user key.
func MakeSearchKey(userKey []byte) InternalKey {
	return MakeInternalKey(userKey, SeqNumMax, InternalKeyKindMax)
}

// DecodeInternalKey decodes an encoded internal key. See InternalKey.Encode().
func DecodeInternalKey(encodedKey []byte) InternalKey {
	n := len(encodedKey) - InternalTrailerLen
	var trailer InternalKeyTrailer
	if n >= 0 {
		trailer = InternalKeyTrailer(binary.LittleEndian.Uint64(encodedKey[n:]))
		encodedKey = encodedKey[:n:n]
	} else {
		trailer = InternalKeyTrailer(InternalKeyKindInvalid)
		encodedKey = nil
	}
	return InternalKey{
		UserKey: encodedKey,
		Trailer: trailer,
	}
}

// InternalCompare compares two internal keys using the specified comparison
// function. For equal user keys, internal keys compare in descending sequence
// number order. For equal user keys and sequence numbers, internal keys
// compare in descending kind order.
func InternalCompare(userCmp Compare, a, b InternalKey) int {
	if x := userCmp(a.UserKey, b.UserKey); x != 0 {
		return x
	}
	// Reverse order for trailer comparison.
	return cmp.Compare(b.Trailer, a.Trailer)
}

// Encode encodes the receiver into the buffer. The buffer must be large enough
// to hold the encoded data. See InternalKey.Size().
func (k InternalKey) Encode(buf []byte) {
	i := copy(buf, k.UserKey)
	binary.LittleEndian.PutUint64(buf[i:], uint64(k.Trailer))
}

// Size returns the encoded size of the key.
func (k InternalKey) Size() int {
	return len(k.UserKey) + InternalTrailerLen
}

// SeqNum returns the sequence number component of the key.
func (k InternalKey) SeqNum() SeqNum {
	return k.Trailer.SeqNum()
}

// Kind returns the kind component of the key.
func (k InternalKey) Kind() InternalKeyKind {
	return k.Trailer.Kind()
}

// Visible returns true if the key is visible at the specified snapshot
// sequence number.
func (k InternalKey) Visible(snapshot SeqNum) bool {
	return k.SeqNum() <= snapshot
}

// Valid returns true if the key has a valid kind.
func (k InternalKey) Valid() bool {
	return k.Kind() <= InternalKeyKindMax
}
