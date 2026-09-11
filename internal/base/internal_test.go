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

// versions of "b" at seq 1..5, plus neighbours, sorted as stored
func TestSearchKeyAtLandsOnNewestVisible(t *testing.T) {
	var keys []InternalKey
	keys = append(keys, MakeInternalKey([]byte("a"), 9, InternalKeyKindSet))
	for s := SeqNum(1); s <= 5; s++ {
		keys = append(keys, MakeInternalKey([]byte("b"), s, InternalKeyKindSet))
	}
	keys = append(keys, MakeInternalKey([]byte("c"), 9, InternalKeyKindSet))
	sort.Slice(keys, func(i, j int) bool {
		return InternalCompare(bytes.Compare, keys[i], keys[j]) < 0
	})

	for snap := SeqNum(0); snap <= 6; snap++ {
		target := MakeSearchKeyAt([]byte("b"), snap)
		idx := sort.Search(len(keys), func(i int) bool {
			return InternalCompare(bytes.Compare, keys[i], target) >= 0
		})
		got := "none"
		if idx < len(keys) && string(keys[idx].UserKey) == "b" {
			got = string(rune('0' + keys[idx].SeqNum()))
		}
		want := "none"
		if snap >= 1 {
			w := snap
			if w > 5 {
				w = 5
			}
			want = string(rune('0' + w))
		}
		if got != want {
			t.Errorf("snapshot %d: landed on b@%s, want b@%s", snap, got, want)
		} else {
			t.Logf("snapshot %d → b@%s", snap, got)
		}
	}
}

// a Set written exactly at the snapshot seqnum must be visible (inclusive bound)
func TestSearchKeyAtIsInclusive(t *testing.T) {
	set := MakeInternalKey([]byte("k"), 7, InternalKeyKindSet)
	del := MakeInternalKey([]byte("k"), 7, InternalKeyKindDelete)
	target := MakeSearchKeyAt([]byte("k"), 7)
	if c := InternalCompare(bytes.Compare, set, target); c < 0 {
		t.Errorf("Set@7 sorts BEFORE search key at snapshot 7 (c=%d) — would be skipped", c)
	}
	if c := InternalCompare(bytes.Compare, del, target); c < 0 {
		t.Errorf("Delete@7 sorts BEFORE search key at snapshot 7 (c=%d) — would be skipped", c)
	}
	// and a version above the snapshot must be skipped
	above := MakeInternalKey([]byte("k"), 8, InternalKeyKindSet)
	if c := InternalCompare(bytes.Compare, above, target); c >= 0 {
		t.Errorf("Set@8 sorts at/after search key at snapshot 7 (c=%d) — would be wrongly visible", c)
	}
}
