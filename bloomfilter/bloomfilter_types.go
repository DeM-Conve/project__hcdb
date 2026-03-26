package bloomfilter

type BloomFilter struct {
	bits    []bool
	numHash int
	size    int
}
