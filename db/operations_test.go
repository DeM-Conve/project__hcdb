package db

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
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

// TestGetStopsAtTombstone: a delete in the memtable over a value already
// flushed to an SSTable must hide it. The re-put at the end checks the
// opposite failure — stopping on a tombstone too eagerly and shadowing the
// key forever.
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

// TestConcurrentWriters guards the WAL against interleaved writes. Put and
// Delete used to take no lock at all, so two goroutines shared one
// bufio.Writer and tore each other's records apart on disk. Run with -race.
func TestConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	database := openTestDB(t, dir)

	const writers, perWriter = 4, 200

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := fmt.Sprintf("w%d-k%03d", w, i)
				if err := database.Put(key, "v-"+key); err != nil {
					t.Errorf("put %s: %v", key, err)
					return
				}
			}
		}(w)
	}

	// read concurrently too — Get takes RLock, writers take Lock
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < perWriter; i++ {
			database.Get(fmt.Sprintf("w0-k%03d", i))
		}
	}()

	wg.Wait()
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// every record must survive a restart, which only holds if the WAL is
	// well-formed all the way through
	reopened := openTestDB(t, dir)
	defer reopened.Close()
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			key := fmt.Sprintf("w%d-k%03d", w, i)
			assertGet(t, reopened, key, "v-"+key, true)
		}
	}
}

// TestOversizedWriteRejected covers a silent data-loss path: the WAL enforces
// the size limits on Replay but nothing enforced them on write, so an
// over-long key was accepted, then treated as corruption on restart — taking
// every record written after it down with it.
func TestOversizedWriteRejected(t *testing.T) {
	dir := t.TempDir()
	database := openTestDB(t, dir)

	if err := database.Put(strings.Repeat("k", config.MAX_KEY_LENGTH+1), "v"); err != ErrKeyTooLong {
		t.Fatalf("oversized key: got %v, want ErrKeyTooLong", err)
	}
	if err := database.Delete(strings.Repeat("k", config.MAX_KEY_LENGTH+1)); err != ErrKeyTooLong {
		t.Fatalf("oversized delete key: got %v, want ErrKeyTooLong", err)
	}
	if err := database.Put("k", strings.Repeat("v", config.MAX_VALUE_LENGTH+1)); err != ErrValueTooLong {
		t.Fatalf("oversized value: got %v, want ErrValueTooLong", err)
	}

	// exactly at the limit must still be accepted
	if err := database.Put(strings.Repeat("k", config.MAX_KEY_LENGTH), "v"); err != nil {
		t.Fatalf("key at the limit was rejected: %v", err)
	}

	// a rejected write must not have reached the WAL, so nothing after it is lost
	database.Put("before", "safe")
	database.Put(strings.Repeat("k", config.MAX_KEY_LENGTH+1), "oops")
	database.Put("after", "also-safe")
	database.Close()

	reopened := openTestDB(t, dir)
	defer reopened.Close()
	assertGet(t, reopened, "before", "safe", true)
	assertGet(t, reopened, "after", "also-safe", true)
}

// TestDeleteTriggersFlush: tombstones are real memtable entries under MVCC, so
// a delete-only workload has to be able to cross the flush threshold. Delete
// used to skip the size check entirely.
func TestDeleteTriggersFlush(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	// one tombstone is len(key)+8 bytes, so size the loop off the threshold
	// rather than hardcoding a count
	const keyLen = 32
	perEntry := keyLen + base.InternalTrailerLen
	n := (config.DEFAULT_MEMTABLE_FLUSH_SIZE / perEntry) + 100

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%0*d", keyLen, i)
		if err := database.Delete(key); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}

	if got := database.memtable.Size(); got >= config.DEFAULT_MEMTABLE_FLUSH_SIZE {
		t.Fatalf("memtable never flushed: size %d >= threshold %d", got, config.DEFAULT_MEMTABLE_FLUSH_SIZE)
	}
}
