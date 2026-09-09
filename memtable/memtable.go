package memtable

import (
	"bytes"

	"github.com/google/btree"
	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Less orders by user key ascending, then sequence number descending, so the
// newest version of a key sorts first.
func (a Item) Less(b btree.Item) bool {
	return base.InternalCompare(
		bytes.Compare,
		base.DecodeInternalKey(a.Key),
		base.DecodeInternalKey(b.(Item).Key),
	) < 0
}

func NewMemTable() *MemTable {
	return &MemTable{
		tree: btree.New(config.DEFAULT_BTREE_DEGREE),
	}
}

// Put inserts an already-encoded internal key. Each sequence number produces a
// distinct key, so versions accumulate instead of replacing each other.
func (memTable *MemTable) Put(internalKey []byte, value []byte) {
	memTable.insert(internalKey, value)
}

// Delete inserts a tombstone. The key's trailer already marks it as a delete,
// so this differs from Put only in carrying no value.
func (memTable *MemTable) Delete(internalKey []byte) {
	memTable.insert(internalKey, nil)
}

func (memTable *MemTable) insert(internalKey []byte, value []byte) {
	memTable.mut.Lock()
	defer memTable.mut.Unlock()

	newItem := Item{Key: internalKey, Value: value}

	memTable.tree.ReplaceOrInsert(newItem)
	memTable.size += (len(newItem.Key) + len(newItem.Value))
}

func (memTable *MemTable) Get(userKey []byte) ([]byte, bool) {
	memTable.mut.RLock()
	defer memTable.mut.RUnlock()

	var found *Item

	// The search key sorts before every real version of userKey, so the first
	// entry the walk lands on is the newest version.
	memTable.tree.AscendGreaterOrEqual(Item{Key: encodeSearchKey(userKey)}, func(i btree.Item) bool {
		item := i.(Item)
		if !bytes.Equal(base.DecodeInternalKey(item.Key).UserKey, userKey) {
			return false // walked past this user key entirely
		}
		found = &item
		return false
	})

	if found == nil {
		return nil, false
	}
	if base.DecodeInternalKey(found.Key).Kind() == base.InternalKeyKindDelete {
		return nil, false // tombstone
	}

	return found.Value, true
}

func (memTable *MemTable) Size() int {
	memTable.mut.RLock()
	defer memTable.mut.RUnlock()

	return memTable.size
}

// Ascend walks entries in internal-key order, so keys arrive with their
// trailers intact (kind included) and versions of one user key arrive
// newest-first.
func (memTable *MemTable) Ascend(fn func(key, value []byte) bool) {
	memTable.mut.RLock()
	defer memTable.mut.RUnlock()

	memTable.tree.Ascend(func(i btree.Item) bool {
		item := i.(Item)
		return fn(item.Key, item.Value)
	})
}

func encodeSearchKey(userKey []byte) []byte {
	k := base.MakeSearchKey(userKey)
	buf := make([]byte, k.Size())
	k.Encode(buf)
	return buf
}
