// Command hcdb-server runs HCDB as a RESP-speaking TCP server — point
// redis-cli at it once it's up:
//
//	go run ./cmd/hcdb-server
//	redis-cli -p 6380 SET foo bar
//	redis-cli -p 6380 GET foo
//	redis-cli -p 6380 SCAN a z
//
// Configuration is via environment variables, not flags or hardcoded paths —
// a container should be configurable without rebuilding the image:
//
//	HCDB_ADDR      listen address              default ":6380"
//	HCDB_DATA_DIR  directory for WAL + SSTables default "assets"
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/DeM-Conve/project__hcdb/config"
	"github.com/DeM-Conve/project__hcdb/db"
	"github.com/DeM-Conve/project__hcdb/server"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := getenv("HCDB_ADDR", ":6380")
	dataDir := getenv("HCDB_DATA_DIR", "assets")

	database, err := db.Open(config.Config{
		WALPath: filepath.Join(dataDir, "server.wal"),
		SSTDir:  filepath.Join(dataDir, "server-sstables"),
	})
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	srv := server.New(database)

	// SIGTERM is what Docker/Kubernetes send on a normal stop — this is what
	// makes `docker stop` (and orderly rolling restarts) trigger the graceful
	// shutdown path in server.Serve instead of the container being killed
	// after its stop-grace-period expires.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("hcdb-server listening on %s (data dir: %s)", addr, dataDir)
	if err := srv.Serve(ctx, addr); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Println("hcdb-server shut down cleanly")
}
