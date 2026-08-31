package bench

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/db"
)

// ─── helpers ────────────────────────────────────────────────────────────────

// TODO: hardcoded real-disk path so benchmarks run on real disk without any
const benchDiskRoot = "/home/harsh-chauhan/z_drive/PERSONAL/project__hcdb/.benchdata"

func openDB(t testing.TB) (*db.DB, func()) {
	dir := filepath.Join(benchDiskRoot, t.Name())

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("clean bench dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create bench dir: %v", err)
	}

	walPath := filepath.Join(dir, "main.wal")
	sstDir := filepath.Join(dir, "sstables")

	database, err := db.Open(config.Config{WALPath: walPath, SSTDir: sstDir})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.RemoveAll(dir)
	}

	return database, cleanup
}

func randomNumberGenerator() *rand.Rand {
	return rand.New(rand.NewSource(42))
}

// ─── sequential write ────────────────────────────────────────────────────────

func BenchmarkPutSequential(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%08d", i)
		if err := database.Put(key, "value-benchmark-payload-32bytes!!"); err != nil {
			b.Fatalf("put: %v", err)
		}
	}
}

// ─── random write ────────────────────────────────────────────────────────────

// BenchmarkPutRandom measures write throughput with random keys.
// Stresses compaction more than sequential because key distribution is scattered.
func BenchmarkPutRandom(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	var rng *rand.Rand = randomNumberGenerator()

	keys := make([]string, b.N)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%016d", rng.Int63())
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := database.Put(keys[i], "value-benchmark-payload-32bytes!!"); err != nil {
			b.Fatalf("put: %v", err)
		}
	}
}

// ─── sequential read (hot — key exists in memtable) ─────────────────────────

// BenchmarkGetMemtableHit measures read latency when the key is in memtable.
// Best-case read path: no SSTable I/O at all.
func BenchmarkGetMemtableHit(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	// write without flushing so all keys stay in memtable
	const keyCount = 10_000
	for i := 0; i < keyCount; i++ {
		database.Put(fmt.Sprintf("key-%08d", i), "value")
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%08d", i%keyCount)
		database.Get(key)
	}
}

// ─── read after flush (cold — key is in SSTable) ────────────────────────────

// BenchmarkGetSSTableHit measures read latency when the key has been flushed
// to disk. Exercises: index binary search + block read + block decode.
func BenchmarkGetSSTableHit(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	const keyCount = 10_000
	for i := 0; i < keyCount; i++ {
		database.Put(fmt.Sprintf("key-%08d", i), "value")
	}
	// force to disk so reads hit SSTable, not memtable
	if err := database.ForceFlush(); err != nil {
		b.Fatalf("flush: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%08d", i%keyCount)
		database.Get(key)
	}
}

// ─── negative read (key does not exist) ──────────────────────────────────────

// BenchmarkGetMiss measures the worst-case read: key absent.
// Must scan ALL SSTables before returning not-found.
// This is what Bloom filters fix — good to show the before number.
func BenchmarkGetMiss(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	// seed with keys 0..9999, flushed to multiple SSTables
	for i := 0; i < 10_000; i++ {
		database.Put(fmt.Sprintf("key-%08d", i), "value")
	}
	database.ForceFlush()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// keys 1M+ will never exist
		key := fmt.Sprintf("key-%08d", 1_000_000+i)
		database.Get(key)
	}
}

// BenchmarkGetMissScaled shows how miss latency grows with SSTable count.
// This is the benchmark that makes the Bloom filter argument concrete —
// miss cost is O(number of SSTables) with no filter.
func BenchmarkGetMissScaled(b *testing.B) {
	for _, sstCount := range []int{1, 5, 10, 20} {
		sstCount := sstCount
		b.Run(fmt.Sprintf("SSTables-%d", sstCount), func(b *testing.B) {
			database, cleanup := openDB(b)
			defer cleanup()

			// create exactly sstCount SSTables
			for s := 0; s < sstCount; s++ {
				for i := 0; i < 500; i++ {
					database.Put(fmt.Sprintf("sst%d-key-%08d", s, i), "value")
				}
				database.ForceFlush()
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// key that will never exist
				database.Get(fmt.Sprintf("zzz-missing-%08d", i))
			}
		})
	}
}

// ─── delete ──────────────────────────────────────────────────────────────────

// BenchmarkDelete measures tombstone write throughput.
// Same path as Put (WAL + memtable) but writes a tombstone entry.
func BenchmarkDelete(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	// pre-populate so deletes have real keys to target
	for i := 0; i < b.N; i++ {
		database.Put(fmt.Sprintf("key-%08d", i), "value")
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		database.Delete(fmt.Sprintf("key-%08d", i))
	}
}

// ─── mixed workload (80% write, 20% read) ────────────────────────────────────

// BenchmarkMixed simulates a realistic write-heavy workload.
// 80% puts, 20% gets against recently written keys.
func BenchmarkMixed(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	// seed some keys so reads have something to find
	for i := 0; i < 1000; i++ {
		database.Put(fmt.Sprintf("key-%08d", i), "seed-value")
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if i%5 == 0 {
			// 20% reads
			database.Get(fmt.Sprintf("key-%08d", i%1000))
		} else {
			// 80% writes
			database.Put(fmt.Sprintf("key-%08d", i), "value")
		}
	}
}

// ─── compaction stress ───────────────────────────────────────────────────────

// BenchmarkCompactionThroughput measures write throughput when compaction
// fires repeatedly. Forces a flush every 500 ops to trigger compaction often.
func BenchmarkCompactionThroughput(b *testing.B) {
	database, cleanup := openDB(b)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		database.Put(fmt.Sprintf("key-%08d", i), "value")
		if i > 0 && i%500 == 0 {
			database.ForceFlush()
		}
	}
}
