package base

// KEY_LOOKUP_ENUM is the result of a point lookup in one level of the LSM
// tree — the memtable or a single SSTable. Must stay three-valued all the way
// up to db.Get: collapsing KEY_DELETED into KEY_ABSENT lets a tombstone get
// skipped and the value it hid resurface from an older level.
type KEY_LOOKUP_ENUM uint8

const (
	// KEY_ABSENT — this level says nothing about the key; keep searching
	// older levels.
	KEY_ABSENT KEY_LOOKUP_ENUM = iota

	// KEY_FOUND — this level holds a live value; it is the newest one, so
	// stop.
	KEY_FOUND

	// KEY_DELETED — this level holds a tombstone. The key is deleted as of
	// this level, so stop: anything older is superseded.
	KEY_DELETED
)
