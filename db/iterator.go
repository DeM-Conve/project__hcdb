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
	return db.ScanAt(lowerBound, upperBound, base.SeqNumMax)
}

// ScanAt is Scan restricted to versions visible at snapshot.
//
// The filtering happens inside each source iterator rather than here: a source
// never yields a version newer than the snapshot, so by the time entries reach
// the heap the newest one for a user key is already the newest *visible* one,
// and the merge logic needs no snapshot awareness at all.
func (db *DB) ScanAt(lowerBound, upperBound []byte, snapshot base.SeqNum) (*MergeIterator, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	m := &MergeIterator{upperBound: upperBound}

	memIt := memtable.NewIterator(db.memtable, lowerBound, upperBound, snapshot)
	if memIt.Valid() {
		heap.Push(&m.h, &mergeItem{key: memIt.Key(), priority: 0, src: memIt})
	}

	for i, sst := range db.sstables {
		sstIt, err := sstable.NewIterator(sst, lowerBound, snapshot)
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
		ik := base.DecodeInternalKey(top.key)
		userKey, kind := ik.UserKey, ik.Kind()
		val := top.src.Value() // read before advancing the source

		// Every remaining entry for this user key is an older version that the
		// one just popped supersedes — skip them all. That includes further
		// versions inside top's own source, not just duplicates in other
		// sources, since one source can hold many versions of a key.
		skipPast(userKey, top, &m.h)
		for m.h.Len() > 0 && bytes.Equal(base.DecodeInternalKey(m.h[0].key).UserKey, userKey) {
			skipPast(userKey, heap.Pop(&m.h).(*mergeItem), &m.h)
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

// skipPast advances item's source beyond every entry for userKey, then puts
// the source back on the heap if it still has data.
func skipPast(userKey []byte, item *mergeItem, h *mergeHeap) {
	for item.src.Next(); item.src.Valid(); item.src.Next() {
		if !bytes.Equal(base.DecodeInternalKey(item.src.Key()).UserKey, userKey) {
			break
		}
	}
	if item.src.Valid() {
		heap.Push(h, &mergeItem{key: item.src.Key(), priority: item.priority, src: item.src})
	}
}
