package db

import (
	"fmt"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
)

// Size limits are enforced here, on the way in, because the WAL enforces them
// again on the way out: Replay treats an over-long key or value as corruption
// and truncates the log at that point. Accepting an oversized write would
// therefore return nil to the caller and then silently discard that record and
// every record after it on the next restart.
var (
	ErrKeyTooLong   = fmt.Errorf("key exceeds %d bytes", config.MAX_KEY_LENGTH)
	ErrValueTooLong = fmt.Errorf("value exceeds %d bytes", config.MAX_VALUE_LENGTH)
)

// GetSnapshot returns a snapshot of the database as it stands right now.
//
// A snapshot is just the current sequence number. Every write already carries
// a strictly increasing one, so "the state at sequence S" is fully described
// by S alone — there is nothing to copy, allocate or release. Reads taken at S
// are unaffected by anything written afterwards.
//
// NOTE: a snapshot is not yet protected from compaction, which drops
// superseded versions without consulting any reader. Holding one across a
// flush can therefore lose the version it was meant to see. Making that safe
// is the job of the GC boundary (curriculum step 7).
func (db *DB) GetSnapshot() base.SeqNum {
	return base.SeqNum(db.seqNum.Load())
}

// Get returns the newest live value for key.
func (db *DB) Get(key string) ([]byte, bool) {
	return db.GetAt(key, base.SeqNumMax)
}

// GetAt returns the newest value for key that was written at or before
// snapshot, searching newest level first: memtable, then SSTables in
// newest-to-oldest order.
//
// A tombstone in any level ends the search. Without that, a delete recorded in
// the memtable would be skipped over and an older SSTable copy returned.
//
// Reading at base.SeqNumMax is exactly reading the latest state, so Get is
// just this function with a snapshot that can see everything — the two share
// one implementation rather than diverging.
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

// Put writes key/value at the next sequence number.
//
// db.mu is held for writing across the whole call. The WAL's bufio.Writer is
// not safe for concurrent use, and the sequence number must reach the log in
// assignment order — two writers left unsynchronised interleave mid-record and
// corrupt the file on disk.
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

// Delete writes a tombstone at the next sequence number. Nothing is removed —
// the tombstone shadows older versions until compaction can drop them.
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

// flushIfFullLocked flushes once the memtable crosses the size threshold.
//
// Deletes are checked as well as puts: a tombstone is a real memtable entry
// that accumulates like any other version, so a delete-only workload would
// otherwise grow the memtable without bound and never persist it.
//
// Caller must hold db.mu for writing.
func (db *DB) flushIfFullLocked() error {
	if db.memtable.Size() < config.DEFAULT_MEMTABLE_FLUSH_SIZE {
		return nil
	}
	return db.flushMemtableLocked()
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
