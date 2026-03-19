package memtable

import (
	"sync"

	"github.com/google/btree"
)

type Item struct {
	Type  uint8
	Key   []byte
	Value []byte
}

type MemTable struct {
	mut  sync.RWMutex
	tree *btree.BTree
	size int
}
