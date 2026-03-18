package memtable

import (
	"sync"

	"github.com/google/btree"
)

const DEGREE = 32

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
