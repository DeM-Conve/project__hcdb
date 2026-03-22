package sstable

// KEY_LOOKUP_ENUM distinguishes missing keys from explicit tombstones (required for LSM Get).
type KEY_LOOKUP_ENUM uint8
const (
	KEY_ABSENT KEY_LOOKUP_ENUM = iota
	KEY_FOUND
	KEY_DELETED
)

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

type SSTable struct {
	FilePath string
	index    []IndexEntry
}
