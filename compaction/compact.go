package compaction

import (
	"fmt"
	"os"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/sstable"
)

func Compact(tables []*sstable.SSTable, dirPath string) ([]*sstable.SSTable, error) {
	if len(tables) < config.DEFAULT_COMPACTION_THRESHOLD {
		return tables, nil
	}

	groups := groupBySimilarSize(tables)

	var result []*sstable.SSTable
	compacted := make(map[string]bool)

	for _, group := range groups {
		if len(group) < 2 {
			result = append(result, group...)
			continue
		}

		merged, err := mergeGroup(group, dirPath)
		if err != nil {
			return nil, err
		}

		result = append(result, merged)

		for _, sst := range group {
			compacted[sst.FilePath] = true
		}
	}

	// delete old SSTable files that got compacted
	for _, sst := range tables {
		if compacted[sst.FilePath] {
			if err := os.Remove(sst.FilePath); err != nil {
				return nil, fmt.Errorf("failed to delete old SSTable %s: %w", sst.FilePath, err)
			}
		}
	}

	return result, nil
}
