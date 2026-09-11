package base

import "sync"

// Watermark tracks which snapshot sequence numbers are still in use, so
// compaction knows which old versions are still unsafe to drop.
//
// Same shape as BadgerDB's y/watermark.go, simplified: that one runs a
// background goroutine over a min-heap so Floor is O(1). Here Floor just
// scans the map — active snapshots are expected to be few.
type Watermark struct {
	mu     sync.Mutex
	active map[SeqNum]int // refcount per seq — Begin can repeat for the same seq
}

func NewWatermark() *Watermark {
	return &Watermark{active: make(map[SeqNum]int)}
}

// Begin pins seq. GetSnapshot calls this with the seqnum it hands back.
func (w *Watermark) Begin(seq SeqNum) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active[seq]++
}

// Done releases one pin on seq. Call once per Begin.
func (w *Watermark) Done(seq SeqNum) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active[seq] <= 1 {
		delete(w.active, seq)
		return
	}
	w.active[seq]--
}

// Floor returns the smallest pinned sequence number — anything at or below
// it may still be what the oldest snapshot needs. With nothing pinned it
// returns SeqNumMax, so everything superseded is droppable, same as before
// snapshots existed.
func (w *Watermark) Floor() SeqNum {
	w.mu.Lock()
	defer w.mu.Unlock()

	floor := SeqNumMax
	for seq := range w.active {
		if seq < floor {
			floor = seq
		}
	}
	return floor
}
