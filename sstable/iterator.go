package sstable

import (
	"bytes"

	"github.com/hchauhan7816/hcdb/internal/base"
)

// Iterator walks an SSTable's entries in internal-key order, starting from
// the first key >= lowerBound (a user key). It is one "source" in a k-way
// merge, and the keys it yields are encoded internal keys.
type Iterator struct {
	sst      *SSTable
	blockIdx int
	entries  []BlockEntry
	pos      int
	valid    bool
}

func NewIterator(sst *SSTable, lowerBound []byte) (*Iterator, error) {
	it := &Iterator{sst: sst}

	seekKey := base.DecodeInternalKey(EncodeSearchKey(lowerBound))

	it.blockIdx = searchIndex(sst.index, EncodeSearchKey(lowerBound))
	if it.blockIdx < 0 {
		it.blockIdx = 0 // lowerBound is before the first block's first key
	}

	if err := it.loadBlock(); err != nil {
		return nil, err
	}

	for it.valid {
		current := base.DecodeInternalKey(it.entries[it.pos].Key)
		if base.InternalCompare(bytes.Compare, current, seekKey) >= 0 {
			break
		}
		if !it.advance() {
			break
		}
	}

	return it, nil
}

func (it *Iterator) loadBlock() error {
	if it.blockIdx >= len(it.sst.index) {
		it.valid = false
		return nil
	}

	entries, err := it.sst.readBlock(it.sst.index[it.blockIdx])
	if err != nil {
		return err
	}

	it.entries = entries
	it.pos = 0
	it.valid = len(entries) > 0
	return nil
}

// advance moves to the next entry, crossing into the next block if needed.
// Returns false once the SSTable is exhausted.
func (it *Iterator) advance() bool {
	it.pos++
	if it.pos < len(it.entries) {
		return true
	}

	it.blockIdx++
	if err := it.loadBlock(); err != nil || !it.valid {
		it.valid = false
		return false
	}
	return true
}

func (it *Iterator) Valid() bool   { return it.valid }
func (it *Iterator) Key() []byte   { return it.entries[it.pos].Key }
func (it *Iterator) Value() []byte { return it.entries[it.pos].Value }
func (it *Iterator) Type() uint8   { return it.entries[it.pos].Type }
func (it *Iterator) Next()         { it.advance() }
