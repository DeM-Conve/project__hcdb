package db

import (
	"fmt"
	"testing"

	"github.com/hchauhan7816/hcdb/internal/base"
)

func assertGetAt(t *testing.T, database *DB, key string, snap base.SeqNum, wantVal string, wantOK bool) {
	t.Helper()
	got, ok := database.GetAt(key, snap)
	if ok != wantOK || (wantOK && string(got) != wantVal) {
		t.Fatalf("GetAt(%q, %d) = %q,%v; want %q,%v", key, snap, got, ok, wantVal, wantOK)
	}
}

func collectAt(t *testing.T, database *DB, lo, hi []byte, snap base.SeqNum) []string {
	t.Helper()
	it, err := database.ScanAt(lo, hi, snap)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var out []string
	for it.Next() {
		out = append(out, fmt.Sprintf("%s=%s", it.Key(), it.Value()))
	}
	return out
}

// TestSnapshotIsolation is curriculum Feature 9 step 8: write the same key
// three times, take a snapshot between two of the writes, and read at that
// snapshot AFTER all the writes have landed.
func TestSnapshotIsolation(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "v1")
	database.Put("k", "v2")
	snap := database.GetSnapshot() // taken between write 2 and write 3
	database.Put("k", "v3")

	// the snapshot read must not see v3, even though it happened first in
	// wall-clock terms and is already durable in the memtable
	assertGetAt(t, database, "k", snap, "v2", true)
	assertGet(t, database, "k", "v3", true) // a normal read still sees the newest

	// a snapshot from before the key existed sees nothing
	assertGetAt(t, database, "k", 0, "", false)
}

// TestSnapshotSeesDeletesCorrectly: a tombstone is a version like any other,
// so a snapshot taken before the delete must still see the value.
func TestSnapshotSeesDeletesCorrectly(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "alive")
	beforeDelete := database.GetSnapshot()
	database.Delete("k")
	afterDelete := database.GetSnapshot()

	assertGetAt(t, database, "k", beforeDelete, "alive", true)
	assertGetAt(t, database, "k", afterDelete, "", false)
	assertGet(t, database, "k", "", false)

	// re-put after the delete: each snapshot still sees its own era
	database.Put("k", "reborn")
	assertGetAt(t, database, "k", beforeDelete, "alive", true)
	assertGetAt(t, database, "k", afterDelete, "", false)
	assertGet(t, database, "k", "reborn", true)
}

// TestSnapshotAcrossFlush is the half of step 8 that reaches disk: the
// snapshot's version must still be readable once it lives in an SSTable
// rather than the memtable.
func TestSnapshotAcrossFlush(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "v1")
	snap := database.GetSnapshot()
	database.Put("k", "v2")

	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// both versions are now in one SSTable, and the older one must win at snap
	assertGetAt(t, database, "k", snap, "v1", true)
	assertGet(t, database, "k", "v2", true)
}

// TestSnapshotSpansMemtableAndSSTable: the old version is on disk, the new one
// is in the memtable. The snapshot has to skip the memtable's version and fall
// through to the SSTable — the case a naive "memtable always wins" read breaks.
func TestSnapshotSpansMemtableAndSSTable(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "old")
	snap := database.GetSnapshot()
	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	database.Put("k", "new") // memtable only

	assertGetAt(t, database, "k", snap, "old", true)
	assertGet(t, database, "k", "new", true)
}

// TestScanAtSnapshot: range scans honour the snapshot too, including keys
// created entirely after it.
func TestScanAtSnapshot(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("a", "a1")
	database.Put("b", "b1")
	snap := database.GetSnapshot()

	database.Put("b", "b2") // overwrite an existing key
	database.Put("c", "c1") // a key that did not exist at snap
	database.Delete("a")    // delete a key that did exist at snap

	got := collectAt(t, database, []byte("a"), []byte("z"), snap)
	want := []string{"a=a1", "b=b1"}
	assertSlice(t, "scan at snapshot", got, want)

	got = collectAt(t, database, []byte("a"), []byte("z"), base.SeqNumMax)
	want = []string{"b=b2", "c=c1"}
	assertSlice(t, "scan at latest", got, want)
}

func assertSlice(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v; want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v; want %v", what, got, want)
		}
	}
}

// TestScanAtSnapshotAfterFlush: the visibility filter must keep working once
// versions are read back off disk, not just while they're in the memtable.
func TestScanAtSnapshotAfterFlush(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("a", "a1")
	database.Put("b", "b1")
	snap := database.GetSnapshot()

	database.Put("b", "b2")
	database.Put("c", "c1")
	database.Delete("a")

	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := collectAt(t, database, []byte("a"), []byte("z"), snap)
	want := []string{"a=a1", "b=b1"}
	assertSlice(t, "scan at snapshot after flush", got, want)

	got = collectAt(t, database, []byte("a"), []byte("z"), base.SeqNumMax)
	want = []string{"b=b2", "c=c1"}
	assertSlice(t, "scan at latest after flush", got, want)
}
