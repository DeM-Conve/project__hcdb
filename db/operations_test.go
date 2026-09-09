package db

import (
	"path/filepath"
	"testing"

	"github.com/hchauhan7816/hcdb/config"
)

func openTestDB(t *testing.T, dir string) *DB {
	t.Helper()
	conf := config.Config{
		WALPath: filepath.Join(dir, "main.wal"),
		SSTDir:  filepath.Join(dir, "sstables"),
	}
	database, err := Open(conf)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return database
}

func assertGet(t *testing.T, database *DB, key, wantVal string, wantOK bool) {
	t.Helper()
	got, ok := database.Get(key)
	if ok != wantOK || (wantOK && string(got) != wantVal) {
		t.Fatalf("Get(%q) = %q,%v; want %q,%v", key, got, ok, wantVal, wantOK)
	}
}

// TestGetStopsAtTombstone covers the case that made a three-valued
// memtable.Get necessary: a delete recorded in the memtable, sitting above a
// value already flushed to an SSTable. While memtable.Get reported "absent"
// and "deleted" identically, db.Get skipped the tombstone, searched the
// SSTables, and returned the value the delete was meant to hide.
//
// The re-put at the end is the opposite direction: a fix that stopped on a
// tombstone too eagerly would shadow the key forever.
func TestGetStopsAtTombstone(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("gone", "back")
	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	database.Delete("gone")
	assertGet(t, database, "gone", "", false)

	database.Put("gone", "again")
	assertGet(t, database, "gone", "again", true)
}

// TestGetStopsAtTombstoneAfterRestart is the same case, but the tombstone
// reaches the memtable through WAL replay rather than a live Delete — a
// separate path, since rebuildMemtable derives the kind from the stored key.
func TestGetStopsAtTombstoneAfterRestart(t *testing.T) {
	dir := t.TempDir()

	database := openTestDB(t, dir)
	database.Put("gone", "back")
	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	database.Delete("gone")
	database.Close()

	reopened := openTestDB(t, dir)
	defer reopened.Close()
	assertGet(t, reopened, "gone", "", false)
}
