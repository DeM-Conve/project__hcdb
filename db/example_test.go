package db_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DeM-Conve/project__hcdb/config"
	"github.com/DeM-Conve/project__hcdb/db"
)

// Example demonstrates using HCDB as an embedded Go library: opening a
// database, writing through several memtable flushes (triggering
// compaction), then reading and range-scanning the result.
//
// To run HCDB as a standalone RESP server instead, see cmd/hcdb-server.
func Example() {
	dir, err := os.MkdirTemp("", "hcdb-example")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer os.RemoveAll(dir)

	database, err := db.Open(config.Config{
		WALPath: filepath.Join(dir, "main.wal"),
		SSTDir:  filepath.Join(dir, "sstables"),
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer database.Close()

	database.Put("user:1", "alice")
	database.Put("user:2", "bob")
	database.Put("user:3", "carol")
	database.ForceFlush()

	database.Put("user:1", "alice-updated")
	database.Put("user:4", "dave")
	database.ForceFlush()

	database.Delete("user:2")
	database.ForceFlush()

	if val, ok := database.Get("user:1"); ok {
		fmt.Printf("user:1 => %s\n", val)
	}
	if _, ok := database.Get("user:2"); !ok {
		fmt.Println("user:2 => not found")
	}

	it, err := database.Scan([]byte("user:1"), []byte("user:9"))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for it.Next() {
		fmt.Printf("%s => %s\n", it.Key(), it.Value())
	}

	// Output:
	// user:1 => alice-updated
	// user:2 => not found
	// user:1 => alice-updated
	// user:3 => carol
	// user:4 => dave
}
