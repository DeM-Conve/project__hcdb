package db

import "github.com/hchauhan7816/hcdb/config"

func (db *DB) Get(key string) ([]byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if val, ok := db.memtable.Get([]byte(key)); ok {
		return val, true
	}

	return db.searchSSTables([]byte(key))
}

func (db *DB) Put(key, value string) error {
	if err := db.wal.Put(key, value); err != nil {
		return err
	}

	db.memtable.Put([]byte(key), []byte(value))

	if db.memtable.Size() >= config.DEFAULT_MEMTABLE_FLUSH_SIZE {
		return db.flushMemtable()
	}

	return nil
}

func (db *DB) Delete(key string) error {
	if err := db.wal.Delete(key); err != nil {
		return err
	}

	db.memtable.Delete([]byte(key))

	return nil
}
