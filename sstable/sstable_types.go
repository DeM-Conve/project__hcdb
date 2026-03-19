package sstable

type BlockEntry struct {
	Key   []byte
	Value []byte
	Type  uint8
}

type IndexEntry struct {
	FirstKey []byte
	Offset   int64
	Length   int32
}
