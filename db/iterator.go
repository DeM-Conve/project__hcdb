package db

import (
	"bytes"
	"container/heap"

	"github.com/hchauhan7816/hcdb/internal/base"
	"github.com/hchauhan7816/hcdb/memtable"
	"github.com/hchauhan7816/hcdb/sstable"
)

// source is anything the merging iterator can pull sorted entries from — the
// memtable and each SSTable all implement this identically. Keys are encoded
// internal keys, so the kind comes from the key rather than a separate field.
type source interface {
	Valid() bool
	Key() []byte
	Value() []byte
	Next()
}

type mergeItem struct {
	key      []byte
	priority int // lower = newer; wins ties on duplicate keys
	src      source
}

type mergeHeap []*mergeItem

func (h mergeHeap) Len() int { return len(h) }

// Less orders by internal key: user key ascending, then sequence number
// descending, so the newest version of a key pops first. priority only breaks
// ties between identical internal keys, which happens when the same write is
// present in both a replayed memtable and an already-flushed SSTable.
func (h mergeHeap) Less(i, j int) bool {
	c := base.InternalCompare(
		bytes.Compare,
		base.DecodeInternalKey(h[i].key),
		base.DecodeInternalKey(h[j].key),
	)
	if c != 0 {
		return c < 0
	}
	return h[i].priority < h[j].priority
}

func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *mergeHeap) Push(x any) {
	*h = append(*h, x.(*mergeItem))
}

func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// MergeIterator walks the memtable and every SSTable together in sorted
// key order, newest-wins on duplicate keys, tombstones hidden.
type MergeIterator struct {
	h          mergeHeap
	upperBound []byte
	key, val   []byte
	valid      bool
}

// Scan returns an iterator over [lowerBound, upperBound] across the
// memtable and all SSTables, merged in sorted order.
func (db *DB) Scan(lowerBound, upperBound []byte) (*MergeIterator, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	m := &MergeIterator{upperBound: upperBound}

	memIt := memtable.NewIterator(db.memtable, lowerBound, upperBound)
	if memIt.Valid() {
		heap.Push(&m.h, &mergeItem{key: memIt.Key(), priority: 0, src: memIt})
	}

	for i, sst := range db.sstables {
		sstIt, err := sstable.NewIterator(sst, lowerBound)
		if err != nil {
			return nil, err
		}
		if sstIt.Valid() {
			heap.Push(&m.h, &mergeItem{key: sstIt.Key(), priority: i + 1, src: sstIt})
		}
	}

	return m, nil
}

// Next advances to the next surviving key. Returns false once the scan is
// exhausted or has passed upperBound.
func (m *MergeIterator) Next() bool {
	for m.h.Len() > 0 {
		top := heap.Pop(&m.h).(*mergeItem)
		userKey := base.DecodeInternalKey(top.key).UserKey

		// Older versions of the same user key, and duplicates from other
		// sources: advance past them without emitting. The newest version
		// popped first, so everything else here is superseded.
		for m.h.Len() > 0 && bytes.Equal(base.DecodeInternalKey(m.h[0].key).UserKey, userKey) {
			dup := heap.Pop(&m.h).(*mergeItem)
			dup.src.Next()
			if dup.src.Valid() {
				heap.Push(&m.h, &mergeItem{key: dup.src.Key(), priority: dup.priority, src: dup.src})
			}
		}

		val := top.src.Value()
		kind := base.DecodeInternalKey(top.key).Kind()
		top.src.Next()
		if top.src.Valid() {
			heap.Push(&m.h, &mergeItem{key: top.src.Key(), priority: top.priority, src: top.src})
		}

		if m.upperBound != nil && bytes.Compare(userKey, m.upperBound) > 0 {
			m.valid = false
			return false
		}

		if kind == base.InternalKeyKindDelete {
			continue
		}

		m.key, m.val = userKey, val
		m.valid = true
		return true
	}

	m.valid = false
	return false
}

func (m *MergeIterator) Key() []byte   { return m.key }
func (m *MergeIterator) Value() []byte { return m.val }
func (m *MergeIterator) Valid() bool   { return m.valid }
