// Command hcdb-server runs hcdb as a RESP-speaking TCP server — point
// redis-cli at it once it's up:
//
//	go run ./cmd/hcdb-server
//	redis-cli -p 6380 SET foo bar
//	redis-cli -p 6380 GET foo
//	redis-cli -p 6380 SCAN a z
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/db"
	"github.com/hchauhan7816/hcdb/server"
)

func main() {
	database, err := db.Open(config.Config{
		WALPath: "assets/server.wal",
		SSTDir:  "assets/server-sstables",
	})
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	srv := server.New(database)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Println("hcdb-server listening on :6380")
	if err := srv.Serve(ctx, ":6380"); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Println("hcdb-server shut down cleanly")
}
