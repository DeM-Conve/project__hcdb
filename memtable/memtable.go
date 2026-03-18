package memtable

import (
	"github.com/google/btree"
	"github.com/hchauhan7816/hcdb/wal"
)

func (a Item) Less(b btree.Item) bool {
	return string(a.Key) < string(b.(Item).Key)
}

func NewMemTable() *MemTable {
	return &MemTable{
		tree: btree.New(DEGREE),
	}
}

func (memTable *MemTable) Put(key, value []byte) {
	memTable.mut.Lock()
	defer memTable.mut.Unlock()

	newItem := Item{Key: key, Value: value, Type: 0}

	old := memTable.tree.Get(newItem)
	if old != nil {
		oldItem := old.(Item)
		memTable.size -= (len(oldItem.Key) + len(oldItem.Value))
	}

	memTable.tree.ReplaceOrInsert(newItem)
	memTable.size += (len(newItem.Key) + len(newItem.Value))
}

func (memTable *MemTable) Get(key []byte) ([]byte, bool) {
	memTable.mut.RLock()
	defer memTable.mut.RUnlock()

	result := memTable.tree.Get(Item{Key: key})
	if result == nil {
		return nil, false
	}

	item := result.(Item)
	if item.Type == wal.OP_DELETE {
		return nil, false // tombstone
	}

	return item.Value, true
}

func (memTable *MemTable) Delete(key []byte) {
	memTable.mut.Lock()
	defer memTable.mut.Unlock()

	searchItem := Item{Key: key}

	old := memTable.tree.Get(searchItem)
	if old != nil {
		oldItem := old.(Item)
		memTable.size -= (len(oldItem.Key) + len(oldItem.Value))
	}

	tombstone := Item{Key: key, Value: nil, Type: 1}
	memTable.tree.ReplaceOrInsert(tombstone)

	memTable.size += len(tombstone.Key)
}

func (memTable *MemTable) Size() int {
	memTable.mut.RLock()
	defer memTable.mut.RUnlock()

	return memTable.size
}

func (memTable *MemTable) Ascend(fn func(key, value []byte, itemType uint8) bool) {
	memTable.mut.RLock()
	defer memTable.mut.RUnlock()

	memTable.tree.Ascend(func(i btree.Item) bool {
		item := i.(Item)
		return fn(item.Key, item.Value, item.Type)
	})
}
