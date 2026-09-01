package db

import (
	"path/filepath"
	"testing"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/sstable"
)

// TestCrashBetweenFlushAndWALReset simulates a crash inside flushMemtable,
// right after the SSTable is durably installed but before resetWAL() runs.
// Confirms the DB still recovers correct data on restart — the WAL replay
// re-inserts the same entries the SSTable already has, which is redundant
// but not corrupting, since nothing else could write between those two
// steps (flushMemtable holds db.mu for the whole call).
func TestCrashBetweenFlushAndWALReset(t *testing.T) {
	dir := t.TempDir()
	conf := config.Config{
		WALPath: filepath.Join(dir, "main.wal"),
		SSTDir:  filepath.Join(dir, "sstables"),
	}

	database, err := Open(conf)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	keys := []string{"key-0", "key-1", "key-2"}
	for _, k := range keys {
		if err := database.Put(k, "value-"+k); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	// Below DEFAULT_SYNC_THRESHOLD, so force the WAL to actually reach
	// disk — otherwise these puts only exist in the in-process buffer,
	// and reopening the same file wouldn't see them at all.
	if err := database.wal.Sync(); err != nil {
		t.Fatalf("wal sync: %v", err)
	}

	// Manually run just the first half of flushMemtable: install the
	// SSTable, but deliberately skip resetWAL() — simulating a crash
	// between those two steps.
	if _, err := sstable.Flush(database.memtable, database.conf.SSTDir); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Simulate the process restarting: open a fresh DB over the same
	// paths. The WAL still has all 3 puts (never truncated), and the
	// SSTable directory now also has the just-installed SSTable.
	recovered, err := Open(conf)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	for _, k := range keys {
		val, ok := recovered.Get(k)
		if !ok {
			t.Fatalf("expected %s to survive the crash, got miss", k)
		}
		if string(val) != "value-"+k {
			t.Fatalf("key %s: got %q, want %q", k, val, "value-"+k)
		}
	}

	// document the known redundancy: the data now exists in both the
	// WAL-replayed memtable and the installed SSTable.
	if recovered.memtable.Size() == 0 {
		t.Fatalf("expected WAL replay to repopulate the memtable, it's empty")
	}
	if len(recovered.sstables) != 1 {
		t.Fatalf("expected 1 installed SSTable, got %d", len(recovered.sstables))
	}
}
