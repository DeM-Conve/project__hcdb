# Bloom Filter

Package: `bloomfilter/` — files: `bloomfilter_types.go`, `bloom.go`,
`bloomfilter_precompute.go`, `bloomfilter_operations.go`, `hash.go`

## Why it exists

In an LSM tree, a read for a key that doesn't exist is the worst case: it has to check the
memtable and then **every** SSTable on disk, each one costing a file open, a seek, a 4 KB block
read and a decode. With 10 SSTables that's 10 disk hits to conclude "not found".

The bloom filter is a small in-memory probabilistic set that answers "is this key in this
SSTable?" with:

- **"definitely not"** → skip the file entirely, zero disk I/O
- **"probably yes"** → do the real lookup (which may still turn up nothing — a false positive)

**False positives are possible; false negatives are not.** That asymmetry is what makes it safe:
a "no" is always trustworthy, so we can act on it, and a "yes" costs at worst the disk read we'd
have done anyway.

## Type

```go
type BloomFilter struct {
    bits    []bool
    numHash int   // k — hash functions per key
    size    int   // m — number of bits
}
```

One is built per SSTable at write time and serialised into the file; `sstable.Open` deserialises
it back and keeps it in memory for the table's lifetime.

## Sizing the filter (`bloomfilter_precompute.go`)

`NewBloomFilter(expectedKeys)` computes optimal `m` and `k` from the standard formulas, using
`config.DEFAULT_BLOOM_FALSE_POSITIVE_RATE` (= 0.01, i.e. 1%):

```
m = -(n * ln p) / (ln 2)²        bits
k = (m / n) * ln 2               hash functions
```

For `n = 100 000`, `p = 0.01`:

```
m ≈ 958 506 bits  (~117 KB serialised)
k ≈ 7 hash functions
```

Both are rounded up with `math.Ceil`.

Intuition for the formulas: `m/n` is bits-per-key and drives the error rate (~9.6 bits per key
for 1%); `k = (m/n)·ln2` is the number of hashes that minimises the false-positive rate for that
much space. Too few hashes and distinct keys collide too easily; too many and the bit array
saturates with 1s.

## Hashing — double hashing (`hash.go`)

We need `k` (≈7) independent hash values per key but we don't want 7 hash implementations. The
standard trick, **double hashing**, derives all of them from two:

```go
h(key, i) = (h1(key) + i * h2(key)) % size
```

```go
h1 = fnv.New64a()   // FNV-1a 64-bit
h2 = fnv.New32a()   // FNV-1a 32-bit
combined := (h1 + uint64(seed)*uint64(h2)) % uint64(size)
```

This is provably as good as `k` independent hashes for bloom filter purposes (Kirsch–Mitzenmacher).
FNV-1a is chosen because it's fast, non-cryptographic, and in the standard library — there's no
adversary here, only a need for good distribution.

Note both hashes are recomputed inside `hashPosition` on every call, so `Add`/`MightContain`
hash the key `2k` (≈14) times rather than twice. Correct, just wasteful.

## Operations (`bloomfilter_operations.go`)

```go
func (bf *BloomFilter) Add(key []byte) {
    for i := 0; i < bf.numHash; i++ {
        bf.bits[hashPosition(key, i, bf.size)] = true
    }
}

func (bf *BloomFilter) MightContain(key []byte) bool {
    for i := 0; i < bf.numHash; i++ {
        if !bf.bits[hashPosition(key, i, bf.size)] {
            return false      // definitely absent — one zero bit is proof
        }
    }
    return true               // probably present
}
```

`MightContain` short-circuits on the first zero bit, so negative lookups — the common case and
the whole point — are usually cheaper than the full `k` probes.

There is no `Remove`. You can't clear a bit without possibly breaking some other key that shares
it, which would create a false *negative*. Deletion is handled by tombstones at the SSTable
layer instead, and tombstoned keys are still `Add`ed to the filter (`Flush` adds every key it
iterates, regardless of type) — which is required, since a lookup must be able to *find* the
tombstone.

## Serialisation (`hash.go`)

In memory `bits` is a `[]bool` — **1 byte per bit**, 8× larger than necessary. `Serialize` packs
it down to 1 bit per bit for disk:

```
+------------------+------------------+------------------------+
| size (uint32,4B) | numHash(uint32,4B) | packed bits (m/8 bytes) |
+------------------+------------------+------------------------+
```

```go
numBytes := (len(bf.bits) + 7) / 8      // ceiling division
out := make([]byte, numBytes+8)         // +8 for the two metadata fields
...
out[8+i/8] |= 1 << (i % 8)              // bit i → byte i/8, position i%8
```

`Deserialize` reverses it, reading `size` and `numHash` from the header so the reconstructed
filter probes exactly the same positions. Storing the parameters rather than recomputing them
means a change to `DEFAULT_BLOOM_EXPECTED_KEYS` or the false-positive rate doesn't invalidate
existing SSTables — each file carries its own parameters.

The `[]bool` in-memory representation costs ~958 KB of RAM per open SSTable at the current
sizing (vs ~117 KB on disk). A `[]uint64` bitset would remove that 8× overhead.

## Integration with SSTable

**Write** (`sstable/writer.go`, `sstable/write_from_entries.go`):
```go
bloom := bloomfilter.NewBloomFilter(config.DEFAULT_BLOOM_EXPECTED_KEYS)
// ... bloom.Add(key) for every entry ...
bloomBytes := bloomfilter.Serialize(bloom)
// written after the index; bloomOffset recorded in the footer
```

**Read** (`sstable/reader.go`): `readBloom` seeks to `bloomOffset`, reads `bloomLen` + bytes,
`Deserialize`s. Held in `SSTable.bloom`.

**Lookup** (`sstable/sstable.go`) — the filter is checked *first*, before any disk access:
```go
if !sst.bloom.MightContain(key) {
    return nil, KEY_ABSENT, nil     // no file open, no seek, no block read
}
```

## Known limitations

- **Every SSTable is sized for 100 000 keys**, regardless of how many keys it actually holds.
  A flush of a small memtable still writes a ~117 KB filter and holds ~958 KB in RAM. Sizing
  from the actual entry count would fix both. Conversely, a table with far *more* than 100 000
  keys gets a much worse false-positive rate than the configured 1%.
- **`[]bool` backing array** — 8× the memory a bitset would use.
- **Hashes recomputed per probe** — `h1`/`h2` should be computed once per key and reused across
  the `k` iterations.
- **No `Deserialize` length validation** — a truncated filter silently deserialises with the
  missing tail as zeros (the `byteIdx < len(data)` guard), which produces false *negatives*:
  real keys reported absent. Corrupt bloom bytes are not detected.

## Related

- [sstable.md](sstable.md) — where the filter sits in the read path and the file layout
- [config.md](config.md) — `DEFAULT_BLOOM_FALSE_POSITIVE_RATE`, `DEFAULT_BLOOM_EXPECTED_KEYS`
