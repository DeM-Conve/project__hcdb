package sstable

import (
	"os"
	"sort"
)

// ============================================================
// SSTable Read Flow:
//
// 1. Open file
// 2. Read footer (last 12 bytes)
//      → get indexOffset and numEntries
// 3. Load index into memory
// 4. Binary search index to find correct block
// 5. Read only that block from disk
// 6. Scan block entries to find key
//
// ------------------------------------------------------------
// Lookup Flow:
//
//   key → searchIndex() → blockIdx
//       → readBlock(offset, length)
//       → decodeBlock()
//       → findInBlock()
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

	indexOffset, numEntries, err := readFooter(file)
	if err != nil {
		return nil, err
	}

	index, err := decodeIndex(file, indexOffset, numEntries)
	if err != nil {
		return nil, err
	}

	return &SSTable{FilePath: filepath, index: index}, nil
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
