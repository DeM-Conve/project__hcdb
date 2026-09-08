package base

import (
	"bytes"
	"sort"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := MakeInternalKey([]byte("harsh"), 47, InternalKeyKindSet)

	buf := make([]byte, original.Size())
	original.Encode(buf)
	decoded := DecodeInternalKey(buf)

	if string(decoded.UserKey) != "harsh" {
		t.Fatalf("user key: got %q, want %q", decoded.UserKey, "harsh")
	}
	if decoded.SeqNum() != 47 {
		t.Fatalf("seqnum: got %d, want 47", decoded.SeqNum())
	}
	if decoded.Kind() != InternalKeyKindSet {
		t.Fatalf("kind: got %d, want %d", decoded.Kind(), InternalKeyKindSet)
	}
}

func TestInternalCompareIsUserKeyThenNewestFirst(t *testing.T) {
	keys := []InternalKey{
		MakeInternalKey([]byte("b"), 15, InternalKeyKindSet),
		MakeInternalKey([]byte("a"), 9, InternalKeyKindSet),
		MakeInternalKey([]byte("b"), 200, InternalKeyKindSet),
		MakeInternalKey([]byte("a"), 47, InternalKeyKindSet),
	}

	sort.Slice(keys, func(i, j int) bool {
		return InternalCompare(bytes.Compare, keys[i], keys[j]) < 0
	})

	want := []struct {
		userKey string
		seqNum  SeqNum
	}{
		{"a", 47}, // newest version of "a" first
		{"a", 9},
		{"b", 200}, // "b" sorts after "a" despite the far higher seqnum
		{"b", 15},
	}

	for i, w := range want {
		if string(keys[i].UserKey) != w.userKey || keys[i].SeqNum() != w.seqNum {
			t.Fatalf("position %d: got %s@%d, want %s@%d",
				i, keys[i].UserKey, keys[i].SeqNum(), w.userKey, w.seqNum)
		}
	}
}

func TestSearchKeySortsBeforeEveryVersion(t *testing.T) {
	search := MakeSearchKey([]byte("a"))
	newest := MakeInternalKey([]byte("a"), SeqNumMax-1, InternalKeyKindSet)

	if InternalCompare(bytes.Compare, search, newest) >= 0 {
		t.Fatalf("search key must sort before the newest real version of the same user key")
	}
}

func TestVisible(t *testing.T) {
	const snapshot SeqNum = 145

	if !MakeInternalKey([]byte("a"), 9, InternalKeyKindSet).Visible(snapshot) {
		t.Fatalf("seq 9 should be visible at snapshot 145")
	}
	if !MakeInternalKey([]byte("a"), 145, InternalKeyKindSet).Visible(snapshot) {
		t.Fatalf("seq 145 should be visible at snapshot 145 (inclusive)")
	}
	if MakeInternalKey([]byte("a"), 146, InternalKeyKindSet).Visible(snapshot) {
		t.Fatalf("seq 146 must NOT be visible at snapshot 145")
	}
}

func TestDecodeShortKeyIsInvalid(t *testing.T) {
	k := DecodeInternalKey([]byte("abc")) // shorter than InternalTrailerLen

	if k.Valid() {
		t.Fatalf("a key shorter than the trailer must decode as invalid")
	}
}
