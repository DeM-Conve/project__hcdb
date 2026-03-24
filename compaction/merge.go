package compaction

import (
	"fmt"
	"time"

	"github.com/hchauhan7816/hcdb/sstable"
)

func mergeGroup(tables []*sstable.SSTable, dirPath string) (*sstable.SSTable, error) {
	iterators := make([]*sstable.BlockIterator, len(tables))
	for i, sst := range tables {
		it, err := sstable.NewBlockIterator(sst)
		if err != nil {
			return nil, err
		}
		iterators[i] = it
	}

	merged := mergeIterators(iterators)

	filePath := fmt.Sprintf("%s/%d.sst", dirPath, time.Now().UnixNano())
	return sstable.WriteSSTableFromBlockEntries(filePath, merged)
}

// mergeIterators does a k-way merge — newest SSTable wins on duplicate keys
// tables[0] is newest (front of slice), so it wins ties
func mergeIterators(iterators []*sstable.BlockIterator) []sstable.BlockEntry {
	seen := make(map[string]bool)
	var result []sstable.BlockEntry

	for _, it := range iterators {
		for _, entry := range it.Entries {
			key := string(entry.Key)
			if seen[key] {
				continue
			}
			seen[key] = true
			// Tombstones are preserved in compacted output intentionally.
			// It is only safe to drop a tombstone when we are certain no older
			// SSTable at any level can still contain the key. Without a manifest
			// tracking which files have been fully merged, we cannot guarantee this.
			// Premature tombstone removal = deleted keys resurrect from older SSTables.
			result = append(result, entry)
		}
	}

	sstable.SortEntries(result)
	return result
}
