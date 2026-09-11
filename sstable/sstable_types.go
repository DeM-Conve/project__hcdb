package sstable

import (
	"github.com/hchauhan7816/hcdb/bloomfilter"
	"github.com/hchauhan7816/hcdb/cache"
)

type BlockEntry struct {
	Key   []byte // encoded internal key; its trailer carries the kind
	Value []byte
}

type IndexEntry struct {
	FirstKey []byte // first encoded internal key in the block
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
