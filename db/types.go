package db

import (
	"sync"
	"sync/atomic"

	"github.com/DeM-Conve/project__hcdb/cache"
	"github.com/DeM-Conve/project__hcdb/config"
	"github.com/DeM-Conve/project__hcdb/internal/base"
	"github.com/DeM-Conve/project__hcdb/memtable"
	"github.com/DeM-Conve/project__hcdb/sstable"
	"github.com/DeM-Conve/project__hcdb/wal"
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
