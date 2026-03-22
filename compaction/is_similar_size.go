package compaction

import "github.com/hchauhan7816/hcdb/config"

func isSimilarSize(a, b int64) bool {
	if a == 0 || b == 0 {
		return false
	}
	if a < b {
		a, b = b, a
	}
	return a/b <= config.DEFAULT_SIMILAR_SIZE_RATIO
}
