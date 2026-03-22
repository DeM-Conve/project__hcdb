package sstable

import "sort"

func SortEntries(entries []BlockEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return string(entries[i].Key) < string(entries[j].Key)
	})
}
