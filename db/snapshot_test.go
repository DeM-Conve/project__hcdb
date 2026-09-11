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

func TestSnapshotIsolation(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "v1")
	database.Put("k", "v2")
	snap := database.GetSnapshot()
	database.Put("k", "v3")

	assertGetAt(t, database, "k", snap, "v2", true)
	assertGet(t, database, "k", "v3", true)
	assertGetAt(t, database, "k", 0, "", false)
}

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

	database.Put("k", "reborn")
	assertGetAt(t, database, "k", beforeDelete, "alive", true)
	assertGetAt(t, database, "k", afterDelete, "", false)
	assertGet(t, database, "k", "reborn", true)
}

func TestSnapshotAcrossFlush(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "v1")
	snap := database.GetSnapshot()
	database.Put("k", "v2")

	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	assertGetAt(t, database, "k", snap, "v1", true)
	assertGet(t, database, "k", "v2", true)
}

// old version on disk, new version in the memtable — the snapshot has to
// fall through to disk instead of the memtable winning by default.
func TestSnapshotSpansMemtableAndSSTable(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("k", "old")
	snap := database.GetSnapshot()
	if err := database.ForceFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	database.Put("k", "new")

	assertGetAt(t, database, "k", snap, "old", true)
	assertGet(t, database, "k", "new", true)
}

func TestScanAtSnapshot(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	defer database.Close()

	database.Put("a", "a1")
	database.Put("b", "b1")
	snap := database.GetSnapshot()

	database.Put("b", "b2")
	database.Put("c", "c1")
	database.Delete("a")

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

// same as TestScanAtSnapshot but reads back off disk after a flush.
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
