package db

import (
	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

func (db *DB) Get(key string) ([]byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if val, ok := db.memtable.Get([]byte(key)); ok {
		return val, true
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
