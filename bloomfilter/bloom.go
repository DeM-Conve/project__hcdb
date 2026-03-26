package bloomfilter

import (
	"github.com/hchauhan7816/hcdb/config"
)

// BloomFilter is a probabilistic data structure to check key existence.
// False positives possible, false negatives are not.
//
// How it works:
//   - Insert: hash key K times → set those K bits to 1
//   - Check:  hash key K times → if ANY bit is 0, key definitely absent
//     → if ALL bits are 1, key probably present
func NewBloomFilter(expectedKeys int) *BloomFilter {
	size, numHash := findOptimalParamsValues(expectedKeys, config.DEFAULT_BLOOM_FALSE_POSITIVE_RATE)
	return &BloomFilter{
		bits:    make([]bool, size),
		numHash: numHash,
		size:    size,
	}
}
