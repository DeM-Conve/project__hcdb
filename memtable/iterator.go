package memtable

import (
	"bytes"

	"github.com/google/btree"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Iterator eagerly collects the memtable's entries within [lowerBound,
// upperBound] into a sorted slice. Safe to do eagerly here — unlike an
// SSTable, the memtable is already bounded, in-memory data, so there's no
// disk-read cost to defer.
//
// Bounds are user keys; the entries it yields carry encoded internal keys.
// Versions newer than the snapshot are dropped while collecting, so a consumer
// never sees them.
type Iterator struct {
	entries []Item
	pos     int
}

// NewIterator collects entries in [lowerBound, upperBound] that are visible at
// snapshot. Pass base.SeqNumMax to see every version.
//
// Unlike a point lookup, the snapshot cannot be folded into the seek key here:
// the scan crosses many user keys, and each one needs its own version filtered
// independently. So invisible versions are skipped one at a time instead.
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
