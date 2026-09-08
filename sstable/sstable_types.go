package sstable

import (
	"github.com/hchauhan7816/hcdb/bloomfilter"
	"github.com/hchauhan7816/hcdb/cache"
)

type KEY_LOOKUP_ENUM uint8

const (
	KEY_ABSENT KEY_LOOKUP_ENUM = iota
	KEY_FOUND
	KEY_DELETED
)

type BlockEntry struct {
	Key   []byte
	Value []byte
	Type  uint8
}

type IndexEntry struct {
	FirstKey []byte
	Offset   int64
	Length   int32
}

type SSTable struct {
	FilePath string
	index    []IndexEntry
	bloom    *bloomfilter.BloomFilter // loaded from disk on Open
	cache    cache.Cacher             // set via SetCache
}

func (sst *SSTable) SetCache(c cache.Cacher) {
	sst.cache = c
}
