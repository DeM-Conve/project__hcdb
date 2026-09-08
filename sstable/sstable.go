package sstable

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/hchauhan7816/hcdb/config"
)

// Lookup — bloom filter check happens BEFORE any disk read
func (sst *SSTable) Lookup(key []byte) ([]byte, KEY_LOOKUP_ENUM, error) {
	// bloom says definitely not here — skip disk entirely
	if !sst.bloom.MightContain(key) {
		return nil, KEY_ABSENT, nil
	}

	blockIdx := searchIndex(sst.index, key)
	if blockIdx < 0 {
		return nil, KEY_ABSENT, nil
	}

	entries, err := sst.readBlock(sst.index[blockIdx])
	if err != nil {
		return nil, KEY_ABSENT, err
	}

	return findInBlockLookup(entries, key)
}

func (sst *SSTable) Get(key []byte) ([]byte, bool, error) {
	val, st, err := sst.Lookup(key)
	if err != nil {
		return nil, false, err
	}
	if st != KEY_FOUND {
		return nil, false, nil
	}
	return val, true, nil
}

func (sst *SSTable) readBlock(idx IndexEntry) ([]BlockEntry, error) {
	cacheKey := fmt.Sprintf("%s:%d", sst.FilePath, idx.Offset)

	if sst.cache != nil {
		if cached, ok := sst.cache.Get(cacheKey); ok {
			return cached.([]BlockEntry), nil
		}
	}

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

	entries, err := decodeBlock(blockBytes)
	if err != nil {
		return nil, err
	}

	if sst.cache != nil {
		sst.cache.Put(cacheKey, entries)
	}

	return entries, nil
}

func findInBlockLookup(entries []BlockEntry, key []byte) ([]byte, KEY_LOOKUP_ENUM, error) {
	for _, e := range entries {
		if bytes.Equal(e.Key, key) {
			if e.Type == config.OP_DELETE {
				return nil, KEY_DELETED, nil
			}
			return e.Value, KEY_FOUND, nil
		}
	}
	return nil, KEY_ABSENT, nil
}
