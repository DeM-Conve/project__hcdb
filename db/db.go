package db

import (
	"fmt"
	"os"

	"github.com/hchauhan7816/hcdb/cache"
	"github.com/hchauhan7816/hcdb/compaction"
	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
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

	mem, walMaxSeq, err := rebuildMemtable(walObj)
	if err != nil {
		return nil, err
	}

	tables, err := sstable.OpenAllInDir(conf.SSTDir)
	if err != nil {
		return nil, err
	}

	blockCache := cache.NewShardedLRU(
		config.DEFAULT_CACHE_SHARD_COUNT,
		config.DEFAULT_BLOCK_CACHE_ENTRIES/config.DEFAULT_CACHE_SHARD_COUNT,
	)
	for _, sst := range tables {
		sst.SetCache(blockCache)
	}

	sstMaxSeq, err := maxSeqNumInTables(tables)
	if err != nil {
		return nil, err
	}

	db := &DB{
		wal:        walObj,
		memtable:   mem,
		sstables:   tables,
		conf:       conf,
		blockCache: blockCache,
		watermark:  base.NewWatermark(),
	}
	db.seqNum.Store(max(uint64(walMaxSeq), uint64(sstMaxSeq)))

	return db, nil
}

func rebuildMemtable(walObj *wal.WAL) (*memtable.MemTable, base.SeqNum, error) {
	mem := memtable.NewMemTable()

	entries, err := walObj.Replay()
	if err != nil {
		return nil, 0, err
	}

	var maxSeq base.SeqNum

	for _, e := range entries {
		// e.Key is already an encoded internal key, so replay preserves the
		// original sequence numbers rather than assigning new ones.
		if seq := base.DecodeInternalKey(e.Key).SeqNum(); seq > maxSeq {
			maxSeq = seq
		}

		if base.DecodeInternalKey(e.Key).Kind() == base.InternalKeyKindDelete {
			mem.Delete(e.Key)
		} else {
			mem.Put(e.Key, e.Value)
		}
	}

	return mem, maxSeq, nil
}

// maxSeqNumInTables scans every SSTable to recover the highest sequence number
// written. Needed because the WAL is truncated after a flush, so on restart it
// no longer holds the sequence numbers already persisted in SSTables.
//
// NOTE: this is a full O(data) read on open. A manifest recording the last
// sequence number (as LevelDB does) would make it O(1).
func maxSeqNumInTables(tables []*sstable.SSTable) (base.SeqNum, error) {
	var maxSeq base.SeqNum

	for _, sst := range tables {
		it, err := sstable.NewBlockIterator(sst)
		if err != nil {
			return 0, err
		}
		for _, e := range it.Entries {
			if seq := base.DecodeInternalKey(e.Key).SeqNum(); seq > maxSeq {
				maxSeq = seq
			}
		}
	}

	return maxSeq, nil
}

func (db *DB) searchSSTables(key []byte, snapshot base.SeqNum) ([]byte, bool) {
	for _, sst := range db.sstables {
		val, st, err := sst.Lookup(key, snapshot)
		if err != nil {
			return nil, false
		}
		if st == base.KEY_DELETED {
			return nil, false
		}
		if st == base.KEY_FOUND {
			return val, true
		}
	}
	return nil, false
}

func (db *DB) ForceFlush() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.flushMemtableLocked()
}

// flushMemtableLocked writes the memtable out as an SSTable, resets the WAL and
// runs compaction.
//
// The caller must already hold db.mu for writing. It cannot take the lock
// itself: Put calls this while holding db.mu, and Go mutexes are not
// reentrant, so locking here would deadlock instantly.
func (db *DB) flushMemtableLocked() error {
	sst, err := sstable.Flush(db.memtable, db.conf.SSTDir)
	if err != nil {
		return err
	}
	sst.SetCache(db.blockCache)

	db.sstables = append([]*sstable.SSTable{sst}, db.sstables...)
	db.memtable = memtable.NewMemTable()

	if err := db.resetWAL(); err != nil {
		return err
	}

	compacted, err := compaction.Compact(db.sstables, db.conf.SSTDir, db.watermark.Floor())
	if err != nil {
		return err
	}
	for _, sst := range compacted {
		sst.SetCache(db.blockCache)
	}
	db.sstables = compacted

	return nil
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
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.wal.Sync()
}

func (db *DB) PrintMemTable() {
	db.mu.RLock()
	defer db.mu.RUnlock()

	fmt.Println("\n--- Memtable (sorted keys) ---")

	db.memtable.Ascend(func(key, value []byte) bool {
		ik := base.DecodeInternalKey(key)
		if ik.Kind() == base.InternalKeyKindDelete {
			fmt.Printf("%s@%d => [tombstone]\n", ik.UserKey, ik.SeqNum())
		} else {
			fmt.Printf("%s@%d => %s\n", ik.UserKey, ik.SeqNum(), value)
		}
		return true
	})

	fmt.Println("\n--- Memtable Ended ---")
	fmt.Println()
}
