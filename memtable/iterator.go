package memtable

import "github.com/google/btree"

// Iterator eagerly collects the memtable's entries within [lowerBound,
// upperBound] into a sorted slice. Safe to do eagerly here — unlike an
// SSTable, the memtable is already bounded, in-memory data, so there's no
// disk-read cost to defer.
type Iterator struct {
	entries []Item
	pos     int
}

func NewIterator(mt *MemTable, lowerBound, upperBound []byte) *Iterator {
	mt.mut.RLock()
	defer mt.mut.RUnlock()

	it := &Iterator{}
	pivot := Item{Key: lowerBound}

	mt.tree.AscendGreaterOrEqual(pivot, func(i btree.Item) bool {
		item := i.(Item)
		if upperBound != nil && string(item.Key) > string(upperBound) {
			return false
		}
		it.entries = append(it.entries, item)
		return true
	})

	return it
}

func (it *Iterator) Valid() bool   { return it.pos < len(it.entries) }
func (it *Iterator) Key() []byte   { return it.entries[it.pos].Key }
func (it *Iterator) Value() []byte { return it.entries[it.pos].Value }
func (it *Iterator) Type() uint8   { return it.entries[it.pos].Type }
func (it *Iterator) Next()         { it.pos++ }
