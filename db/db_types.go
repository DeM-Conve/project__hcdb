package db

import (
	"github.com/hchauhan7816/hcdb/memtable"
	"github.com/hchauhan7816/hcdb/wal"
)

type DB struct {
	wal      *wal.WAL
	memtable *memtable.MemTable
}
