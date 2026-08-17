package bench

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/db"
)

func openDB(t testing.TB) *db.DB {
	dir := t.TempDir()

	database, err := db.Open(config.Config{WALPath: filepath.Join(dir, "main.wal"), SSTDir: filepath.Join(dir, "sstables")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	return database
}

func BenchmarkPutSequential(b *testing.B) {
	database := openDB(b)
	defer database.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%08d", i)
		if err := database.Put(key, "value-benchmark-payload-32bytes!!"); err != nil {
			b.Fatalf("put: %v", err)
		}
	}
}
