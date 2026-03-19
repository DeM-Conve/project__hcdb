package sstable

import (
	"bytes"
	"io"
	"os"

	"github.com/hchauhan7816/hcdb/config"
)

func (sst *SSTable) Get(key []byte) ([]byte, bool, error) {

	blockIdx := searchIndex(sst.index, key)
	if blockIdx < 0 {
		return nil, false, nil
	}

	entries, err := sst.readBlock(sst.index[blockIdx])
	if err != nil {
		return nil, false, err
	}

	return findInBlock(entries, key)
}

func (sst *SSTable) readBlock(idx IndexEntry) ([]BlockEntry, error) {
	file, err := os.Open(sst.FilePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if _, err := file.Seek(idx.Offset, io.SeekStart); err != nil {
		return nil, err
	}

	blockBytes := make([]byte, idx.Length)
	if _, err := io.ReadFull(file, blockBytes); err != nil {
		return nil, err
	}

	return decodeBlock(blockBytes)
}

func findInBlock(entries []BlockEntry, key []byte) ([]byte, bool, error) {
	for _, e := range entries {
		if bytes.Equal(e.Key, key) {
			if e.Type == config.OP_DELETE {
				return nil, false, nil // tombstone
			}
			return e.Value, true, nil
		}
	}
	return nil, false, nil
}
