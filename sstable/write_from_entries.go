package sstable

import (
	"bufio"
	"os"

	"github.com/hchauhan7816/hcdb/config"
)

func WriteSSTableFromBlockEntries(filePath string, entries []BlockEntry) (*SSTable, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	var indexEntries []IndexEntry
	var currentOffset int64

	collector := newBlockCollector()

	flushCurrent := func() error {
		if collector.len() == 0 {
			return nil
		}

		firstKey := collector.lastFirstKey
		drained := collector.drain()

		newOffset, err := flushBlock(writer, drained, currentOffset)
		if err != nil {
			return err
		}

		indexEntries = append(indexEntries, IndexEntry{
			FirstKey: firstKey,
			Offset:   currentOffset,
			Length:   int32(newOffset - currentOffset),
		})

		currentOffset = newOffset

		return nil
	}

	for _, e := range entries {
		collector.add(e)
		if collector.size() >= config.DEFAULT_BLOCK_SIZE {
			if err := flushCurrent(); err != nil {
				return nil, err
			}
		}
	}

	if err := flushCurrent(); err != nil {
		return nil, err
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
