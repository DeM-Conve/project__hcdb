package sstable

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"github.com/hchauhan7816/hcdb/bloomfilter"
	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
	"github.com/hchauhan7816/hcdb/memtable"
)

// ============================================================
// SSTable Write Flow:
//
// Memtable (sorted by internal key: user key asc, seqNum desc)
//      ↓
// Split into blocks (~4KB each)
//      ↓
// Write blocks sequentially to file
//      ↓
// Build IndexEntry for each block:
//      - FirstKey (the block's first INTERNAL key)
//      - Offset
//      - Length
//      ↓
// Write index section
//      ↓
// Write bloom bytes (BloomLen + BloomBytes)
//      NOTE: the bloom indexes USER keys, not internal keys, because Lookup
//      probes it with a user key before it knows any sequence number.
//      ↓
// Write footer (indexOffset + numEntries + bloomOffset)
//
// ------------------------------------------------------------
// Final File Layout (the canonical picture — other files in this package
// document only their own section and point here):
//
//   +----------------------------+  offset 0
//   | Block 1            (~4KB)  |
//   | Block 2            (~4KB)  |
//   | ...                        |
//   +----------------------------+  ← indexOffset
//   | IndexEntry 1               |
//   | IndexEntry 2               |
//   | ...                        |
//   +----------------------------+  ← bloomOffset
//   | bloomLen (4B) + bloomBytes |
//   +----------------------------+
//   | Footer            (20B)    |
//   +----------------------------+  EOF
//
// Data first, metadata last: block offsets aren't known until the blocks are
// written, so the index can only follow them. The footer sits at a fixed
// offset from EOF — the only section locatable without scanning the file.
//
// See block.go for a block's contents, index.go for IndexEntry and Footer.
// ============================================================

func Flush(memTable *memtable.MemTable, dirPath string) (sst *SSTable, err error) {

	finalPath := fmt.Sprintf("%s/%d.sst", dirPath, time.Now().UnixNano())
	tmpPath := finalPath + ".tmp"

	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	writer := bufio.NewWriter(file)
	bloom := bloomfilter.NewBloomFilter(config.DEFAULT_BLOOM_EXPECTED_KEYS)

	var blockCollector blockCollector
	var indexEntries []IndexEntry
	var currentOffset int64

	var iterErr error
	memTable.Ascend(func(key, value []byte) bool {
		// key is an internal key; the bloom filter is probed with user keys
		bloom.Add(base.DecodeInternalKey(key).UserKey)
		blockCollector.add(BlockEntry{Key: key, Value: value})

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
		lastFirstKey := blockCollector.lastFirstKey
		entries := blockCollector.drain()

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

	// write bloom filter after index
	bloomBytes := bloomfilter.Serialize(bloom)
	bloomOffset := indexOffset + indexSize(indexEntries)
	if err := binary.Write(writer, binary.LittleEndian, uint32(len(bloomBytes))); err != nil {
		return nil, err
	}
	if _, err := writer.Write(bloomBytes); err != nil {
		return nil, err
	}

	// footer now includes bloomOffset
	if err := encodeFooterWithBloom(writer, indexOffset, uint32(len(indexEntries)), bloomOffset); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := atomicInstall(tmpPath, finalPath); err != nil {
		return nil, err
	}

	return &SSTable{FilePath: finalPath, index: indexEntries, bloom: bloom}, nil
}

func indexSize(entries []IndexEntry) int64 {
	var size int64
	for _, e := range entries {
		size += 4 + int64(len(e.FirstKey)) + 8 + 4
	}
	return size
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
