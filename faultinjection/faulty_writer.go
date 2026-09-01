// Package faultinjection simulates storage failures for crash-recovery
// tests. Kept separate from wal/sstable so it never ships in the real path.
package faultinjection

import "io"

// FaultyWriter wraps an io.Writer and fails once more than FailAfter bytes
// have been written.
type FaultyWriter struct {
	w         io.Writer
	written   int
	FailAfter int
}

func NewFaultyWriter(w io.Writer, failAfter int) *FaultyWriter {
	return &FaultyWriter{w: w, FailAfter: failAfter}
}

// Write may succeed partially then still return an error — simulates a
// torn write, not a clean all-or-nothing failure.
func (fw *FaultyWriter) Write(p []byte) (int, error) {
	if fw.written >= fw.FailAfter {
		return 0, io.ErrClosedPipe
	}

	allowed := fw.FailAfter - fw.written
	if allowed >= len(p) {
		n, err := fw.w.Write(p)
		fw.written += n
		return n, err
	}

	// partial write at the boundary
	n, err := fw.w.Write(p[:allowed])
	fw.written += n
	if err != nil {
		return n, err
	}
	return n, io.ErrClosedPipe
}
