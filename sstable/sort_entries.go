package sstable

import (
	"bytes"
	"sort"

	"github.com/hchauhan7816/hcdb/internal/base"
)

func SortEntries(entries []BlockEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return base.InternalCompare(
			bytes.Compare,
			base.DecodeInternalKey(entries[i].Key),
			base.DecodeInternalKey(entries[j].Key),
		) < 0
	})
}
