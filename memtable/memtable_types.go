package memtable

import (
	"sync"

	"github.com/google/btree"
)

type Item struct {
	Type  uint8
	Key   []byte // encoded internal key: user key + 8-byte trailer
	Value []byte
}

type MemTable struct {
	mut  sync.RWMutex
	tree *btree.BTree
	size int
}
