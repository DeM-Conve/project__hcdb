package memtable

import (
	"bytes"

	"github.com/google/btree"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Iterator eagerly collects entries in [lowerBound, upperBound] into a sorted
// slice — safe to do eagerly since the memtable is already in memory. Bounds
// are user keys; entries carry encoded internal keys.
type Iterator struct {
	entries []Item
	pos     int
}

// NewIterator collects entries visible at snapshot (base.SeqNumMax for every
// version). Can't fold the snapshot into one seek key like a point lookup
// does — the scan crosses many user keys, each needing its own filter — so
// invisible versions are skipped one at a time while collecting.
func NewIterator(mt *MemTable, lowerBound, upperBound []byte, snapshot base.SeqNum) *Iterator {
	mt.mut.RLock()
	defer mt.mut.RUnlock()

	it := &Iterator{}
	pivot := Item{Key: encodeSearchKey(lowerBound, snapshot)}

	mt.tree.AscendGreaterOrEqual(pivot, func(i btree.Item) bool {
		item := i.(Item)
		ik := base.DecodeInternalKey(item.Key)

		if upperBound != nil && bytes.Compare(ik.UserKey, upperBound) > 0 {
			return false
		}
		if !ik.Visible(snapshot) {
			return true // too new for this reader — skip, keep scanning
		}
		it.entries = append(it.entries, item)
		return true
	})

	return it
}

func (it *Iterator) Valid() bool   { return it.pos < len(it.entries) }
func (it *Iterator) Key() []byte   { return it.entries[it.pos].Key }
func (it *Iterator) Value() []byte { return it.entries[it.pos].Value }
func (it *Iterator) Next()         { it.pos++ }
