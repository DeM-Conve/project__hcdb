package db

import (
	"fmt"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/memtable"
	"github.com/hchauhan7816/hcdb/wal"
)

func Open(filePath string) (*DB, error) {
	walObj, err := wal.Open(filePath)
	if err != nil {
		return nil, err
	}

	mem := memtable.NewMemTable()

	entries, err := walObj.Replay()
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		switch e.Type {
		case config.OP_DELETE:
			mem.Delete(e.Key)
		case config.OP_PUT:
			mem.Put(e.Key, e.Value)
		}
	}

	return &DB{wal: walObj, memtable: mem}, nil
}

func (db *DB) Put(key, value string) error {
	if err := db.wal.Put(key, value); err != nil {
		return err
	}

	db.memtable.Put([]byte(key), []byte(value))

	return nil
}

func (db *DB) Get(key string) ([]byte, bool) {
	return db.memtable.Get([]byte(key))
}

func (db *DB) Delete(key string) error {
	if err := db.wal.Delete(key); err != nil {
		return err
	}

	db.memtable.Delete([]byte(key))

	return nil
}

func (db *DB) Close() error {
	return db.wal.Sync()
}

func (db *DB) Print() {
	fmt.Println("\n--- sorted keys ---")

	db.memtable.Ascend(func(key, value []byte, itemType uint8) bool {
		if itemType == config.OP_DELETE {
			fmt.Printf("%s => [tombstone]\n", key)
		} else {
			fmt.Printf("%s => %s\n", key, value)
		}
		return true
	})

	fmt.Println()
}
