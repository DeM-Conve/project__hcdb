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
