package db

import (
	"sync"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/memtable"
	"github.com/hchauhan7816/hcdb/sstable"
	"github.com/hchauhan7816/hcdb/wal"
)

type DB struct {
	mu       sync.RWMutex
	wal      *wal.WAL
	memtable *memtable.MemTable
	sstables []*sstable.SSTable
	conf     config.Config
}
