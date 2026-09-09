package memtable

import (
	"sync"

	"github.com/google/btree"
)

type Item struct {
	Key   []byte // encoded internal key; its trailer carries the kind
	Value []byte
}

type MemTable struct {
	mut  sync.RWMutex
	tree *btree.BTree
	size int
}
