package db

import (
	"sync"
	"sync/atomic"

	"github.com/hchauhan7816/hcdb/cache"
	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/internal/base"
	"github.com/hchauhan7816/hcdb/memtable"
	"github.com/hchauhan7816/hcdb/sstable"
	"github.com/hchauhan7816/hcdb/wal"
)

type DB struct {
	mu         sync.RWMutex
	wal        *wal.WAL
	memtable   *memtable.MemTable
	sstables   []*sstable.SSTable
	conf       config.Config
	blockCache cache.Cacher
	seqNum     atomic.Uint64   // last assigned sequence number
	watermark  *base.Watermark // active snapshots — see GetSnapshot/ReleaseSnapshot
}
