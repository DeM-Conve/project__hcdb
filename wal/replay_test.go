package wal

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/hchauhan7816/hcdb/faultinjection"
)

// TestTornWriteRecovery: crash mid-write via FaultyWriter, then assert
// Replay recovers all complete entries and truncates the torn one.
func TestTornWriteRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.wal")

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}

	// 3 entries x 37 bytes (4 totalLen + 29 data + 4 crc) = 111 bytes.
	// FailAfter=120 lets those through, then tears the 4th after 9 bytes.
	faulty := faultinjection.NewFaultyWriter(file, 120)

	// built manually, not via Open, so BufWriter wraps faulty instead of file
	walObj := &WAL{
		File:      file,
		BufWriter: bufio.NewWriter(faulty),
	}

	for i := 0; i < 3; i++ {
		key := []byte("good-key-" + string(rune('0'+i)))
		value := []byte("good-value")
		entry := Entry{Key: key, Value: value}
		if err := walObj.Append(entry); err != nil {
			t.Fatalf("append good entry %d: %v", i, err)
		}
	}
	if err := walObj.BufWriter.Flush(); err != nil {
		t.Fatalf("flush good entries: %v", err)
	}

	tornEntry := Entry{Key: []byte("torn-key-x"), Value: []byte("torn-value")}
	if err := walObj.Append(tornEntry); err != nil {
		t.Fatalf("append torn entry: %v", err)
	}
	if err := walObj.BufWriter.Flush(); err == nil {
		t.Fatalf("expected the flush of the torn entry to fail, got nil error")
	}

	// simulate process restart: fresh WAL, no fault injection
	recovered, err := Open(path)
	if err != nil {
		t.Fatalf("reopen wal: %v", err)
	}

	entries, err := recovered.Replay()
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 recovered entries, got %d: %+v", len(entries), entries)
	}

	for i, e := range entries {
		wantKey := "good-key-" + string(rune('0'+i))
		if string(e.Key) != wantKey || string(e.Value) != "good-value" {
			t.Fatalf("entry %d: got key=%q value=%q, want key=%q value=%q", i, e.Key, e.Value, wantKey, "good-value")
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// 3 clean records survive. Each is totalLen(4) + keyLen(4) + valLen(4) +
	// key(10) + value(10) + crc32(4) = 36 bytes.
	const wantSize = 3 * 36
	if info.Size() != wantSize {
		t.Fatalf("expected file truncated to %d bytes, got %d", wantSize, info.Size())
	}
}
