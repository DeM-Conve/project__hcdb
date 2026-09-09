package db

import (
	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Get returns the newest live value for key, searching newest level first:
// memtable, then SSTables in newest-to-oldest order.
//
// A tombstone in any level ends the search. Without that, a delete recorded in
// the memtable would be skipped over and an older SSTable copy returned.
func (db *DB) Get(key string) ([]byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	switch val, st := db.memtable.Get([]byte(key)); st {
	case base.KEY_FOUND:
		return val, true
	case base.KEY_DELETED:
		return nil, false
	}

	return db.searchSSTables([]byte(key))
}

func (db *DB) Put(key, value string) error {

	var valueByte []byte = []byte(value)
	internalKey := db.encodeNextKey([]byte(key), base.InternalKeyKindSet)

	if err := db.wal.Put(internalKey, valueByte); err != nil {
		return err
	}

	db.memtable.Put(internalKey, valueByte)

	if db.memtable.Size() >= config.DEFAULT_MEMTABLE_FLUSH_SIZE {
		return db.flushMemtable()
	}

	return nil
}

func (db *DB) Delete(key string) error {

	internalKey := db.encodeNextKey([]byte(key), base.InternalKeyKindDelete)

	if err := db.wal.Delete(internalKey); err != nil {
		return err
	}

	db.memtable.Delete(internalKey)

	return nil
}

// encodeNextKey assigns the next sequence number and encodes the internal key
// the WAL and memtable both store.
func (db *DB) encodeNextKey(userKey []byte, kind base.InternalKeyKind) []byte {
	seqNum := base.SeqNum(db.seqNum.Add(1))

	k := base.MakeInternalKey(userKey, seqNum, kind)
	buf := make([]byte, k.Size())
	k.Encode(buf)
	return buf
}
