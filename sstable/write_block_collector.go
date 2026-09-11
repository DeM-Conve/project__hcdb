package sstable

type blockCollector struct {
	entries      []BlockEntry
	sizeEstimate int
	lastFirstKey []byte
}

func newBlockCollector() *blockCollector {
	return &blockCollector{}
}

func (bc *blockCollector) add(e BlockEntry) {

	if len(bc.entries) == 0 {
		bc.lastFirstKey = e.Key
	}

	bc.entries = append(bc.entries, e)
	// e.Key is an internal key, so its 8-byte trailer is already in len(e.Key)
	bc.sizeEstimate += len(e.Key) + len(e.Value) + 8 // keyLen(4)+valLen(4)
}

func (bc *blockCollector) size() int {
	return bc.sizeEstimate
}

func (bc *blockCollector) len() int {
	return len(bc.entries)
}

func (bc *blockCollector) drain() []BlockEntry {
	entries := bc.entries
	bc.entries = bc.entries[:0]
	bc.sizeEstimate = 0
	bc.lastFirstKey = nil
	return entries
}
