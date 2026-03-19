package sstable

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/memtable"
)

// ============================================================
// SSTable Write Flow:
//
// Memtable (sorted)
//      ↓
// Split into blocks (~4KB each)
//      ↓
// Write blocks sequentially to file
//      ↓
// Build IndexEntry for each block:
//      - FirstKey
//      - Offset
//      - Length
//      ↓
// Write index section
//      ↓
// Write footer (indexOffset + numEntries)
//
// Final File:
//
//   [ Block 1 ][ Block 2 ] ... [ Index ][ Footer ]
//
// ============================================================

func Flush(memTable *memtable.MemTable, dirPath string) (*SSTable, error) {

	filePath := fmt.Sprintf("%s/%d.sst", dirPath, time.Now().UnixNano())

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	var blockCollector blockCollector
	var indexEntries []IndexEntry
	var currentOffset int64

	var iterErr error
	memTable.Ascend(func(key, value []byte, itemType uint8) bool {
		blockCollector.add(BlockEntry{Key: key, Value: value, Type: itemType})

		if blockCollector.size() >= config.DEFAULT_BLOCK_SIZE {
			lastFirstKey := blockCollector.lastFirstKey
			entries := blockCollector.drain()

			offset, err := flushBlock(writer, entries, currentOffset)
			if err != nil {
				iterErr = err
				return false
			}

			indexEntries = append(indexEntries, IndexEntry{
				FirstKey: lastFirstKey,
				Offset:   currentOffset,
				Length:   int32(offset - currentOffset),
			})

			currentOffset = offset
		}

		return true
	})

	if iterErr != nil {
		return nil, iterErr
	}

	// flush remaining
	if blockCollector.len() > 0 {
		entries := blockCollector.drain()
		lastFirstKey := blockCollector.lastFirstKey

		offset, err := flushBlock(writer, entries, currentOffset)
		if err != nil {
			return nil, err
		}

		indexEntries = append(indexEntries, IndexEntry{
			FirstKey: lastFirstKey,
			Offset:   currentOffset,
			Length:   int32(offset - currentOffset),
		})

		currentOffset = offset
	}

	indexOffset := currentOffset

	if err := encodeIndex(writer, indexEntries); err != nil {
		return nil, err
	}
	if err := encodeFooter(writer, indexOffset, uint32(len(indexEntries))); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}

	return &SSTable{FilePath: filePath, index: indexEntries}, nil
}

func flushBlock(w *bufio.Writer, entries []BlockEntry, currentOffset int64) (nn int64, err error) {
	blockBytes, err := encodeBlock(entries)
	if err != nil {
		return currentOffset, err
	}

	if _, err := w.Write(blockBytes); err != nil {
		return currentOffset, err
	}

	return currentOffset + int64(len(blockBytes)), nil
}
