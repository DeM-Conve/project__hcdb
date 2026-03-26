package bloomfilter

import "math"

// findOptimalParamsValues computes bit array size and number of hash functions
// given expected number of keys and desired false positive rate.
//
// m = -(n * ln(p)) / (ln(2)^2)
// k = (m/n) * ln(2)
func findOptimalParamsValues(n int, p float64) (size int, numHash int) {
	m := -float64(n) * math.Log(p) / (math.Log(2) * math.Log(2))
	k := (m / float64(n)) * math.Log(2)
	return int(math.Ceil(m)), int(math.Ceil(k))
}
