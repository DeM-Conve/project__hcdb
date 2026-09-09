package sstable

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/hchauhan7816/hcdb/internal/base"
)

// Lookup takes a user key and returns its newest version in this SSTable.
//
//	user key → bloom.MightContain()        (bloom indexes user keys)
//	    ├─ no  → base.KEY_ABSENT      (zero disk I/O)
//	    └─ yes → seek an Iterator to the user key, which lands on its newest
//	             version (or on the next user key if it is absent)
//
// The seek is delegated to Iterator rather than doing a single searchIndex
// lookup, because a search key sorts BEFORE every real version of its user
// key: when a user key happens to be a block's first key, searchIndex points
// at the PREVIOUS block. Iterator walks forward across blocks and lands
// correctly; the same applies when one user key's versions span two blocks.
func (sst *SSTable) Lookup(userKey []byte) ([]byte, base.KEY_LOOKUP_ENUM, error) {
	// bloom says definitely not here — skip disk entirely
	if !sst.bloom.MightContain(userKey) {
		return nil, base.KEY_ABSENT, nil
	}

	it, err := NewIterator(sst, userKey)
	if err != nil {
		return nil, base.KEY_ABSENT, err
	}
	if !it.Valid() {
		return nil, base.KEY_ABSENT, nil
	}

	ik := base.DecodeInternalKey(it.Key())
	if !bytes.Equal(ik.UserKey, userKey) {
		return nil, base.KEY_ABSENT, nil
	}
	if ik.Kind() == base.InternalKeyKindDelete {
		return nil, base.KEY_DELETED, nil
	}
	return it.Value(), base.KEY_FOUND, nil
}

func (sst *SSTable) Get(key []byte) ([]byte, bool, error) {
	val, st, err := sst.Lookup(key)
	if err != nil {
		return nil, false, err
	}
	if st != base.KEY_FOUND {
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

// EncodeSearchKey builds the encoded internal key used to seek to the newest
// version of userKey.
func EncodeSearchKey(userKey []byte) []byte {
	k := base.MakeSearchKey(userKey)
	buf := make([]byte, k.Size())
	k.Encode(buf)
	return buf
}
