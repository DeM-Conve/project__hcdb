package bloomfilter

import (
	"encoding/binary"
	"hash/fnv"
)

// hashPosition uses double hashing
// h(key, i) = (h1(key) + i * h2(key)) % size
//
// This avoids needing K separate hash implementations while giving
// good distribution across the bit array.
func hashPosition(key []byte, seed int, size int) int {
	h1 := fnvHash(key)
	h2 := fnvHash32(key)
	combined := (h1 + uint64(seed)*uint64(h2)) % uint64(size)
	return int(combined)
}

func fnvHash(key []byte) uint64 {
	h := fnv.New64a()
	h.Write(key)
	return h.Sum64()
}

func fnvHash32(key []byte) uint32 {
	h := fnv.New32a()
	h.Write(key)
	return h.Sum32()
}

// Serialize converts bloom filter bits to compact byte slice for disk storage.
// Each byte stores 8 bits — reduces storage from 1 byte/bit to 1 bit/bit.
func Serialize(bf *BloomFilter) []byte {
	numBytes := (len(bf.bits) + 7) / 8
	out := make([]byte, numBytes+8) // +8 for metadata (size + numHash)

	binary.LittleEndian.PutUint32(out[0:4], uint32(bf.size))
	binary.LittleEndian.PutUint32(out[4:8], uint32(bf.numHash))

	for i, bit := range bf.bits {
		if bit {
			out[8+i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// Deserialize reconstructs a BloomFilter from bytes written by Serialize.
func Deserialize(data []byte) *BloomFilter {
	size := int(binary.LittleEndian.Uint32(data[0:4]))
	numHash := int(binary.LittleEndian.Uint32(data[4:8]))

	bits := make([]bool, size)
	for i := 0; i < size; i++ {
		byteIdx := 8 + i/8
		bitIdx := i % 8
		if byteIdx < len(data) {
			bits[i] = (data[byteIdx]>>bitIdx)&1 == 1
		}
	}

	return &BloomFilter{bits: bits, numHash: numHash, size: size}
}
