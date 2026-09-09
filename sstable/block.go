package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// ============================================================
// Block encoding. For where blocks sit inside the file, see writer.go.
//
// Block Format:
//
//   +----------------------+
//   | numEntries (4 bytes) |
//   +----------------------+
//   | entry 1              |
//   | entry 2              |
//   | ...                  |
//   +----------------------+
//   | CRC32 (4 bytes)      |
//   +----------------------+
//
// ------------------------------------------------------------
// BlockEntry (each record inside block):
//
//   +---------+----------+---------------+--------+
//   | keyLen  | valLen   | internal key  | value  |
//   | 4 bytes | 4 bytes  | bytes         | bytes  |
//   +---------+----------+---------------+--------+
//
//   internal key = user key + 8-byte trailer (seqNum<<8 | kind), so keyLen
//   is len(userKey)+8. See internal/base.
//
//
// Notes:
// - Blocks are ~4KB in size
// - CRC is used for corruption detection
// - Entire block is read at once during lookup
// - Entries are sorted by user key ascending, then sequence number
//   descending, so multiple versions of one user key sit next to each other
//   with the newest first
// - There is no type byte: put vs delete lives in the key's trailer
// ============================================================

func encodeBlock(entries []BlockEntry) ([]byte, error) {
	var buf bytes.Buffer

	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(entries))); err != nil {
		return nil, err
	}

	for _, e := range entries {
		if err := writeBlockEntry(&buf, e); err != nil {
			return nil, err
		}
	}

	crc := crc32.ChecksumIEEE(buf.Bytes())
	if err := binary.Write(&buf, binary.LittleEndian, crc); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func writeBlockEntry(buf *bytes.Buffer, e BlockEntry) error {
	if err := binary.Write(buf, binary.LittleEndian, uint32(len(e.Key))); err != nil {
		return err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(len(e.Value))); err != nil {
		return err
	}
	if _, err := buf.Write(e.Key); err != nil {
		return err
	}
	if _, err := buf.Write(e.Value); err != nil {
		return err
	}
	return nil
}

func decodeBlock(blockBytes []byte) ([]BlockEntry, error) {
	if len(blockBytes) < 4 {
		return nil, fmt.Errorf("block too small")
	}

	data := blockBytes[:len(blockBytes)-4]
	storedCRC := binary.LittleEndian.Uint32(blockBytes[len(blockBytes)-4:])

	if crc32.ChecksumIEEE(data) != storedCRC {
		return nil, fmt.Errorf("block CRC mismatch")
	}

	reader := bytes.NewReader(data)

	var numEntries uint32
	if err := binary.Read(reader, binary.LittleEndian, &numEntries); err != nil {
		return nil, err
	}

	entries := make([]BlockEntry, 0, numEntries)
	for i := uint32(0); i < numEntries; i++ {
		e, err := readBlockEntry(reader)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}

	return entries, nil
}

func readBlockEntry(r io.Reader) (BlockEntry, error) {
	var keyLen, valLen uint32

	if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
		return BlockEntry{}, err
	}
	if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
		return BlockEntry{}, err
	}

	key := make([]byte, keyLen)
	val := make([]byte, valLen)

	if _, err := io.ReadFull(r, key); err != nil {
		return BlockEntry{}, err
	}
	if _, err := io.ReadFull(r, val); err != nil {
		return BlockEntry{}, err
	}

	return BlockEntry{Key: key, Value: val}, nil
}
