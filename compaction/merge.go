package compaction

import (
	"fmt"
	"time"

	"github.com/hchauhan7816/hcdb/internal/base"
	"github.com/hchauhan7816/hcdb/sstable"
)

func mergeGroup(tables []*sstable.SSTable, dirPath string, floor base.SeqNum) (*sstable.SSTable, error) {
	iterators := make([]*sstable.BlockIterator, len(tables))
	for i, sst := range tables {
		it, err := sstable.NewBlockIterator(sst)
		if err != nil {
			return nil, err
		}
		iterators[i] = it
	}

	merged := mergeIterators(iterators, floor)

	filePath := fmt.Sprintf("%s/%d.sst", dirPath, time.Now().UnixNano())
	return sstable.WriteSSTableFromBlockEntries(filePath, merged)
}

// mergeIterators does a k-way merge — newest version wins on duplicate user
// keys. tables[0] is newest, entries within a table are newest-first, so the
// first occurrence of a key across all tables is the newest version.
//
// floor is the oldest seqnum any active snapshot still needs (base.Watermark
// .Floor). Everything above floor is kept — some active snapshot between
// floor and now might need any of them, and Floor only reports the minimum,
// not the full set. Once one version at or below floor is kept, every older
// duplicate is unreachable by any snapshot and gets dropped.
//
// No active snapshot means floor is base.SeqNumMax, so the first entry always
// qualifies and this collapses to the old keep-only-newest behavior.
//
// Tombstones are always kept regardless of floor — dropping one requires
// knowing no older SSTable still holds the key, which needs a manifest we
// don't have. Drop one early and a deleted key can resurrect.
func mergeIterators(iterators []*sstable.BlockIterator, floor base.SeqNum) []sstable.BlockEntry {
	keptAtOrBelowFloor := make(map[string]bool)
	var result []sstable.BlockEntry

	for _, it := range iterators {
		for _, entry := range it.Entries {
			ik := base.DecodeInternalKey(entry.Key)
			key := string(ik.UserKey)

			if keptAtOrBelowFloor[key] {
				continue // an older duplicate of a version no active snapshot can still reach
			}

			result = append(result, entry)
			if ik.SeqNum() <= floor {
				keptAtOrBelowFloor[key] = true
			}
		}
	}

	sstable.SortEntries(result)
	return result
}
