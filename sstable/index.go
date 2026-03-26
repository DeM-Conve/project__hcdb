package sstable

import (
	"bytes"
	"encoding/binary"
	"io"
)

// ============================================================
// Index + Footer Layout (on disk)
//
// After all blocks are written, we write:
//
//   +---------------------------+
//   | IndexEntry 1              |
//   | IndexEntry 2              |
//   | ...                       |
//   +---------------------------+
//   | BloomLen + BloomBytes     |
//   +---------------------------+
//   | Footer                    |
//   +---------------------------+
//
// ------------------------------------------------------------
// IndexEntry Format:
//
//   +-----------+-------+--------+--------+
//   | keyLen    | key   | offset | length |
//   | 4 bytes   | bytes | int64  | int32  |
//   +-----------+-------+--------+--------+
//
// Fields:
// - FirstKey → first key of the block
// - Offset   → starting position of block in file
// - Length   → size of block in bytes
//
// Example:
//
//   Block 1: [a, b, c]
//   Block 2: [d, e, f]
//   Block 3: [g, h, i]
//
//   Index:
//
//   [a -> block1]
//   [d -> block2]
//   [g -> block3]
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

func searchIndex(index []IndexEntry, key []byte) int {
	lo, hi := 0, len(index)-1
	result := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if bytes.Compare(index[mid].FirstKey, key) <= 0 {
			result = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return result
}
