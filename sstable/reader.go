package sstable

import (
	"encoding/binary"
	"io"
	"os"
	"sort"

	"github.com/hchauhan7816/hcdb/bloomfilter"
)

// ============================================================
// SSTable Read Flow:
//
// 1. Open file
// 2. Read footer (last 20 bytes)
//      → get indexOffset, numEntries, and bloomOffset
// 3. Load index into memory
// 4. Load Bloom filter from bloomOffset
// 5. On lookup, check Bloom first
// 6. If Bloom may contain key: binary search index, read block, scan block entries
//
// ------------------------------------------------------------
// Lookup Flow:
//
//   key → bloom.MightContain()
//       ├─ no  → KEY_ABSENT (skip disk block read)
//       └─ yes → searchIndex() → blockIdx
//                → readBlock(offset, length)
//                → decodeBlock()
//                → findInBlock()
//
// ------------------------------------------------------------
// Key Insight:
//
// - IndexEntry → helps locate block (log N)
// - BlockEntry → actual data (scan within block)
//
// ============================================================

// Binary search on index entries.
// Returns the index of the block whose FirstKey <= target key.
//
// Example:
//
// Index:
//   [a, d, g]
//
// Search key = "e"
//
// Result:
//   returns index of "d" → block 2
//

func Open(filepath string) (*SSTable, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	indexOffset, numEntries, bloomOffset, err := readFooterWithBloom(file)
	if err != nil {
		return nil, err
	}

	index, err := decodeIndex(file, indexOffset, numEntries)
	if err != nil {
		return nil, err
	}

	bloom, err := readBloom(file, bloomOffset)
	if err != nil {
		return nil, err
	}

	return &SSTable{FilePath: filepath, index: index, bloom: bloom}, nil
}

func readBloom(file *os.File, bloomOffset int64) (*bloomfilter.BloomFilter, error) {
	if _, err := file.Seek(bloomOffset, io.SeekStart); err != nil {
		return nil, err
	}

	var bloomLen uint32
	if err := binary.Read(file, binary.LittleEndian, &bloomLen); err != nil {
		return nil, err
	}

	bloomBytes := make([]byte, bloomLen)
	if _, err := io.ReadFull(file, bloomBytes); err != nil {
		return nil, err
	}

	return bloomfilter.Deserialize(bloomBytes), nil
}

func OpenAllInDir(dirPath string) ([]*SSTable, error) {
	dirEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	// sort ascending by name (UnixNano), then reverse = newest first
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].Name() > dirEntries[j].Name()
	})

	var tables []*SSTable
	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		sst, err := Open(dirPath + "/" + e.Name())
		if err != nil {
			return nil, err
		}
		tables = append(tables, sst)
	}
	return tables, nil
}
