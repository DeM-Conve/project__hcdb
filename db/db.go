package db

import (
	"fmt"
	"os"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/memtable"
	"github.com/hchauhan7816/hcdb/sstable"
	"github.com/hchauhan7816/hcdb/wal"
)

func Open(conf config.Config) (*DB, error) {
	if err := os.MkdirAll(conf.SSTDir, 0755); err != nil {
		return nil, err
	}

	walObj, err := wal.Open(conf.WALPath)
	if err != nil {
		return nil, err
	}

	mem, err := rebuildMemtable(walObj)
	if err != nil {
		return nil, err
	}

	tables, err := sstable.OpenAllInDir(conf.SSTDir)
	if err != nil {
		return nil, err
	}

	return &DB{wal: walObj, memtable: mem, sstables: tables, conf: conf}, nil
}

func rebuildMemtable(walObj *wal.WAL) (*memtable.MemTable, error) {
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

	return mem, nil
}

func (db *DB) searchSSTables(key []byte) ([]byte, bool) {
	for _, sst := range db.sstables {
		val, ok, err := sst.Get(key)
		if err != nil || !ok {
			continue
		}
		return val, true
	}
	return nil, false
}

func (db *DB) ForceFlush() error {
	return db.flushMemtable()
}

func (db *DB) flushMemtable() error {
	sst, err := sstable.Flush(db.memtable, db.conf.SSTDir)
	if err != nil {
		return err
	}

	db.sstables = append([]*sstable.SSTable{sst}, db.sstables...)
	db.memtable = memtable.NewMemTable()

	return db.resetWAL()
}

func (db *DB) resetWAL() error {
	if err := db.wal.File.Truncate(0); err != nil {
		return err
	}
	if _, err := db.wal.File.Seek(0, 0); err != nil {
		return err
	}
	db.wal.BufWriter.Reset(db.wal.File)
	return nil
}

func (db *DB) Close() error {
	return db.wal.Sync()
}

func (db *DB) PrintMemTable() {
	fmt.Println("\n--- Memtable (sorted keys) ---")

	db.memtable.Ascend(func(key, value []byte, itemType uint8) bool {
		if itemType == config.OP_DELETE {
			fmt.Printf("%s => [tombstone]\n", key)
		} else {
			fmt.Printf("%s => %s\n", key, value)
		}
		return true
	})

	fmt.Println("\n--- Memtable Ended ---")
	fmt.Println()
}
