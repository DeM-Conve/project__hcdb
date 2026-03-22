package compaction

import (
	"os"

	"github.com/hchauhan7816/hcdb/sstable"
)

func groupBySimilarSize(tables []*sstable.SSTable) [][]*sstable.SSTable {
	sizes := make([]int64, len(tables))
	for i, sst := range tables {
		info, err := os.Stat(sst.FilePath)
		if err != nil {
			sizes[i] = 0
			continue
		}
		sizes[i] = info.Size()
	}

	var groups [][]*sstable.SSTable
	used := make([]bool, len(tables))

	for i := 0; i < len(tables); i++ {
		if used[i] {
			continue
		}

		group := []*sstable.SSTable{tables[i]}
		used[i] = true

		for j := i + 1; j < len(tables); j++ {
			if used[j] {
				continue
			}
			if isSimilarSize(sizes[i], sizes[j]) {
				group = append(group, tables[j])
				used[j] = true
			}
		}

		groups = append(groups, group)
	}

	return groups
}
