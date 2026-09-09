package config

const (
	MAX_KEY_LENGTH   = 1024
	MAX_VALUE_LENGTH = 1048576

	DEFAULT_BTREE_DEGREE        = 32
	DEFAULT_MEMTABLE_FLUSH_SIZE = 4 * 1024 * 1024 // 4MB
	DEFAULT_BLOCK_SIZE          = 4096            // 4KB
	DEFAULT_SYNC_THRESHOLD      = 10

	DEFAULT_COMPACTION_THRESHOLD = 4 // trigger compaction when this many SSTables exist
	DEFAULT_SIMILAR_SIZE_RATIO   = 2 // two tables are "similar size" if larger/smaller <= this

	DEFAULT_BLOOM_FALSE_POSITIVE_RATE = 0.01   // 1% false positive rate
	DEFAULT_BLOOM_EXPECTED_KEYS       = 100000 // expected keys per SSTable

	DEFAULT_BLOCK_CACHE_ENTRIES = 256 // ~1MB of decoded blocks at DEFAULT_BLOCK_SIZE, total across shards
	DEFAULT_CACHE_SHARD_COUNT   = 16  // DEFAULT_BLOCK_CACHE_ENTRIES / DEFAULT_CACHE_SHARD_COUNT entries per shard
)

type Config struct {
	WALPath string
	SSTDir  string
}

func DefaultConfig(walPath, sstDir string) *Config {
	return &Config{
		WALPath: walPath,
		SSTDir:  sstDir,
	}
}
