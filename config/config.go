package config

const (
	OP_PUT    = uint8(0)
	OP_DELETE = uint8(1)

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
