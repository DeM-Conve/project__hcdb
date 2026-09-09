package base

// KEY_LOOKUP_ENUM is the result of a point lookup in one level of the LSM
// tree — the memtable or a single SSTable.
//
// The three states must stay distinct all the way up to db.Get. Collapsing
// KEY_DELETED into KEY_ABSENT makes a tombstone invisible to the caller, which
// then keeps searching older levels and resurrects the value the tombstone was
// there to hide.
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
