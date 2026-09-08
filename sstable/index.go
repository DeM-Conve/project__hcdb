package sstable

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/hchauhan7816/hcdb/internal/base"
)

// ============================================================
// Index and footer encoding, plus the binary search over the index.
// For where these sections sit inside the file, see writer.go.
//
// IndexEntry Format:
//
//   +-----------+---------------+--------+--------+
//   | keyLen    | internal key  | offset | length |
//   | 4 bytes   | bytes         | int64  | int32  |
//   +-----------+---------------+--------+--------+
//
// Fields:
// - FirstKey → first INTERNAL key of the block (user key + 8-byte trailer),
//              so keyLen covers the trailer too
// - Offset   → starting position of block in file
// - Length   → size of block in bytes
//
// Example (a@N means user key "a" at sequence number N; a user key can appear
// more than once, newest sequence number first):
//
//   Block 1: [a@7, a@2, b@5]
//   Block 2: [d@9, e@1, f@4]
//   Block 3: [g@8, h@3, i@6]
//
//   Index:
//
//   [a@7 -> block1]
//   [d@9 -> block2]
//   [g@8 -> block3]
//
// Ordering is base.InternalCompare: user key ascending, then sequence number
// descending. Lookups build a search key (user key + max sequence number),
// which sorts BEFORE every real version of that user key — so searchIndex can
// return -1 for a key that still lives in block 0.
//
// ------------------------------------------------------------
// Footer Format (last 20 bytes of file):
//
//   +-------------------+
//   | indexOffset (8B)  |
//   +-------------------+
//   | numEntries (4B)   |
//   +-------------------+
//   | bloomOffset (8B)  |
//   +-------------------+
//
// Purpose:
// - Allows direct jump to index and bloom section without scanning file
//
// ============================================================

func encodeIndex(w io.Writer, entries []IndexEntry) error {
	for _, idx := range entries {
		if err := binary.Write(w, binary.LittleEndian, uint32(len(idx.FirstKey))); err != nil {
			return err
		}
		if _, err := w.Write(idx.FirstKey); err != nil {
			return err
		}
		if err := binary.Write(w, binary.LittleEndian, idx.Offset); err != nil {
			return err
		}
		if err := binary.Write(w, binary.LittleEndian, idx.Length); err != nil {
			return err
		}
	}
	return nil
}

func decodeIndex(r io.ReadSeeker, indexOffset int64, numEntries uint32) ([]IndexEntry, error) {

	if _, err := r.Seek(indexOffset, io.SeekStart); err != nil {
		return nil, err
	}

	index := make([]IndexEntry, 0, numEntries)

	for i := uint32(0); i < numEntries; i++ {
		entry, err := readIndexEntry(r)
		if err != nil {
			return nil, err
		}

		index = append(index, entry)
	}

	return index, nil
}

func readIndexEntry(r io.Reader) (IndexEntry, error) {
	var keyLen uint32
	if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
		return IndexEntry{}, err
	}

	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return IndexEntry{}, err
	}

	var offset int64
	var length int32
	if err := binary.Read(r, binary.LittleEndian, &offset); err != nil {
		return IndexEntry{}, err
	}
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return IndexEntry{}, err
	}

	return IndexEntry{FirstKey: key, Offset: offset, Length: length}, nil
}

func encodeFooter(w io.Writer, indexOffset int64, numEntries uint32) error {
	if err := binary.Write(w, binary.LittleEndian, indexOffset); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, numEntries)
}

func readFooter(r io.ReadSeeker) (indexOffset int64, numEntries uint32, err error) {
	if _, err = r.Seek(-12, io.SeekEnd); err != nil {
		return
	}
	if err = binary.Read(r, binary.LittleEndian, &indexOffset); err != nil {
		return
	}
	err = binary.Read(r, binary.LittleEndian, &numEntries)
	return
}

func encodeFooterWithBloom(w io.Writer, indexOffset int64, numEntries uint32, bloomOffset int64) error {
	if err := binary.Write(w, binary.LittleEndian, indexOffset); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, numEntries); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, bloomOffset)
}

func readFooterWithBloom(r io.ReadSeeker) (indexOffset int64, numEntries uint32, bloomOffset int64, err error) {
	if _, err = r.Seek(-20, io.SeekEnd); err != nil {
		return
	}
	if err = binary.Read(r, binary.LittleEndian, &indexOffset); err != nil {
		return
	}
	if err = binary.Read(r, binary.LittleEndian, &numEntries); err != nil {
		return
	}
	err = binary.Read(r, binary.LittleEndian, &bloomOffset)
	return
}

// searchIndex takes an encoded internal key and returns the last block whose
// first key sorts at or before it.
func searchIndex(index []IndexEntry, key []byte) int {
	target := base.DecodeInternalKey(key)

	lo, hi := 0, len(index)-1
	result := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		firstKey := base.DecodeInternalKey(index[mid].FirstKey)
		if base.InternalCompare(bytes.Compare, firstKey, target) <= 0 {
			result = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return result
}
