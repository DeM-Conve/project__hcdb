package sstable

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Lookup takes a user key and returns its newest version in this SSTable.
//
//	user key → bloom.MightContain()        (bloom indexes user keys)
//	    ├─ no  → KEY_ABSENT                (zero disk I/O)
//	    └─ yes → EncodeSearchKey → searchIndex → blockIdx
//	             → readBlock (via the block cache) → decodeBlock
//	             → findInBlockLookup — first entry matching the user key,
//	               which is its newest version
func (sst *SSTable) Lookup(userKey []byte) ([]byte, KEY_LOOKUP_ENUM, error) {
	// bloom says definitely not here — skip disk entirely
	if !sst.bloom.MightContain(userKey) {
		return nil, KEY_ABSENT, nil
	}

	// A search key sorts before every real version of the same user key, so a
	// -1 here means "before the first block's first key" — the key can still
	// live in block 0, unlike with plain keys where -1 meant absent.
	blockIdx := searchIndex(sst.index, EncodeSearchKey(userKey))
	if blockIdx < 0 {
		if len(sst.index) == 0 {
			return nil, KEY_ABSENT, nil
		}
		blockIdx = 0
	}

	entries, err := sst.readBlock(sst.index[blockIdx])
	if err != nil {
		return nil, KEY_ABSENT, err
	}

	return findInBlockLookup(entries, userKey)
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

// findInBlockLookup returns the newest version of userKey in the block.
// Entries are sorted newest-first within a user key, so the first match wins.
func findInBlockLookup(entries []BlockEntry, userKey []byte) ([]byte, KEY_LOOKUP_ENUM, error) {
	for _, e := range entries {
		if bytes.Equal(base.DecodeInternalKey(e.Key).UserKey, userKey) {
			if e.Type == config.OP_DELETE {
				return nil, KEY_DELETED, nil
			}
			return e.Value, KEY_FOUND, nil
		}
	}
	return nil, KEY_ABSENT, nil
}

// EncodeSearchKey builds the encoded internal key used to seek to the newest
// version of userKey.
func EncodeSearchKey(userKey []byte) []byte {
	k := base.MakeSearchKey(userKey)
	buf := make([]byte, k.Size())
	k.Encode(buf)
	return buf
}
