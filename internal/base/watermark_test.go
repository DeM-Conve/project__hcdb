package base

import "testing"

func TestWatermarkFloorWithNothingPinned(t *testing.T) {
	w := NewWatermark()
	if got := w.Floor(); got != SeqNumMax {
		t.Fatalf("Floor() with nothing pinned = %d, want SeqNumMax", got)
	}
}

func TestWatermarkFloorIsSmallestPinned(t *testing.T) {
	w := NewWatermark()
	w.Begin(10)
	w.Begin(3)
	w.Begin(7)
	if got := w.Floor(); got != 3 {
		t.Fatalf("Floor() = %d, want 3", got)
	}
}

func TestWatermarkFloorAdvancesAsSnapshotsFinish(t *testing.T) {
	w := NewWatermark()
	w.Begin(3)
	w.Begin(7)
	w.Begin(10)

	w.Done(3)
	if got := w.Floor(); got != 7 {
		t.Fatalf("after releasing 3: Floor() = %d, want 7", got)
	}

	w.Done(7)
	if got := w.Floor(); got != 10 {
		t.Fatalf("after releasing 7: Floor() = %d, want 10", got)
	}

	w.Done(10)
	if got := w.Floor(); got != SeqNumMax {
		t.Fatalf("after releasing everything: Floor() = %d, want SeqNumMax", got)
	}
}

// Two callers can be handed the same snapshot number (GetSnapshot returns the
// same seqnum if nothing was written in between). Done must not release the
// pin until every Begin for that number has a matching Done.
func TestWatermarkRefcountsDuplicateSeq(t *testing.T) {
	w := NewWatermark()
	w.Begin(5)
	w.Begin(5) // same seqnum, second reader

	w.Done(5) // first reader finishes
	if got := w.Floor(); got != 5 {
		t.Fatalf("Floor() = %d after one of two Done(5) calls, want 5 (still pinned)", got)
	}

	w.Done(5) // second reader finishes
	if got := w.Floor(); got != SeqNumMax {
		t.Fatalf("Floor() = %d after both Done(5) calls, want SeqNumMax", got)
	}
}

// A Done with no matching Begin must not corrupt state for other snapshots —
// defensive against a caller bug, not something the API is expected to need.
func TestWatermarkDoneWithoutBeginIsHarmless(t *testing.T) {
	w := NewWatermark()
	w.Begin(9)
	w.Done(100) // never begun
	if got := w.Floor(); got != 9 {
		t.Fatalf("Floor() = %d, want 9 (unaffected by the bogus Done)", got)
	}
}
