package db

import (
	"fmt"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Enforced on write too, not just WAL replay — an oversized record accepted
// here would get truncated as "corruption" on the next replay, taking every
// record after it down with it.
var (
	ErrKeyTooLong   = fmt.Errorf("key exceeds %d bytes", config.MAX_KEY_LENGTH)
	ErrValueTooLong = fmt.Errorf("value exceeds %d bytes", config.MAX_VALUE_LENGTH)
)

// GetSnapshot pins the current sequence number so compaction keeps whatever
// versions it still needs. Must be paired with ReleaseSnapshot, or those
// versions never get reclaimed.
func (db *DB) GetSnapshot() base.SeqNum {
	snap := base.SeqNum(db.seqNum.Load())
	db.watermark.Begin(snap)
	return snap
}

// ReleaseSnapshot unpins a snapshot from GetSnapshot.
func (db *DB) ReleaseSnapshot(snap base.SeqNum) {
	db.watermark.Done(snap)
}

func (db *DB) Get(key string) ([]byte, bool) {
	return db.GetAt(key, base.SeqNumMax)
}

// GetAt reads the newest value for key visible at snapshot: memtable first,
// then SSTables newest to oldest. A tombstone at any level ends the search —
// otherwise a memtable delete would get skipped and an older SSTable value
// returned instead.
func (db *DB) GetAt(key string, snapshot base.SeqNum) ([]byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	switch val, st := db.memtable.Get([]byte(key), snapshot); st {
	case base.KEY_FOUND:
		return val, true
	case base.KEY_DELETED:
		return nil, false
	}

	return db.searchSSTables([]byte(key), snapshot)
}

// Put holds db.mu for the whole call — the WAL's bufio.Writer isn't safe for
// concurrent use, and writes need to hit the log in seqnum order.
func (db *DB) Put(key, value string) error {
	if len(key) > config.MAX_KEY_LENGTH {
		return ErrKeyTooLong
	}
	if len(value) > config.MAX_VALUE_LENGTH {
		return ErrValueTooLong
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	var valueByte []byte = []byte(value)
	internalKey := db.encodeNextKey([]byte(key), base.InternalKeyKindSet)

	if err := db.wal.Put(internalKey, valueByte); err != nil {
		return err
	}

	db.memtable.Put(internalKey, valueByte)

	return db.flushIfFullLocked()
}

func (db *DB) Delete(key string) error {
	if len(key) > config.MAX_KEY_LENGTH {
		return ErrKeyTooLong
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	internalKey := db.encodeNextKey([]byte(key), base.InternalKeyKindDelete)

	if err := db.wal.Delete(internalKey); err != nil {
		return err
	}

	db.memtable.Delete(internalKey)

	return db.flushIfFullLocked()
}

// flushIfFullLocked also fires on deletes — a tombstone is a real memtable
// entry under MVCC, so a delete-only workload needs to flush too.
func (db *DB) flushIfFullLocked() error {
	if db.memtable.Size() < config.DEFAULT_MEMTABLE_FLUSH_SIZE {
		return nil
	}
	return db.flushMemtableLocked()
}

func (db *DB) encodeNextKey(userKey []byte, kind base.InternalKeyKind) []byte {
	seqNum := base.SeqNum(db.seqNum.Add(1))

	k := base.MakeInternalKey(userKey, seqNum, kind)
	buf := make([]byte, k.Size())
	k.Encode(buf)
	return buf
}
