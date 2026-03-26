package bloomfilter

func (bf *BloomFilter) Add(key []byte) {
	for i := 0; i < bf.numHash; i++ {
		pos := hashPosition(key, i, bf.size)
		bf.bits[pos] = true
	}
}

func (bf *BloomFilter) MightContain(key []byte) bool {
	for i := 0; i < bf.numHash; i++ {
		pos := hashPosition(key, i, bf.size)
		if !bf.bits[pos] {
			return false
		}
	}
	return true
}
