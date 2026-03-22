package sstable

import (
	"io"
	"os"
)

type BlockIterator struct {
	Entries []BlockEntry
}

func NewBlockIterator(sst *SSTable) (*BlockIterator, error) {
	file, err := os.Open(sst.FilePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []BlockEntry

	for _, idx := range sst.index {
		if _, err := file.Seek(idx.Offset, io.SeekStart); err != nil {
			return nil, err
		}

		blockBytes := make([]byte, idx.Length)
		if _, err := io.ReadFull(file, blockBytes); err != nil {
			return nil, err
		}

		blockEntries, err := decodeBlock(blockBytes)
		if err != nil {
			return nil, err
		}

		entries = append(entries, blockEntries...)
	}

	return &BlockIterator{Entries: entries}, nil
}
