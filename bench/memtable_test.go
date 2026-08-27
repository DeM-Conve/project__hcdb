package bench

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hchauhan7816/hcdb/memtable"
)

func BenchmarkMemtablePut(b *testing.B) {

	mt := memtable.NewMemTable()

	v := []byte("RandomValueKeyCreatedForTesting")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%08d", i))
		mt.Put(key, v)
	}
}

func BenchmarkMemtablePutParallel(b *testing.B) {
	mt := memtable.NewMemTable()

	v := []byte("RandomValueKeyCreatedForTesting")
	var counter atomic.Int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			key := []byte(fmt.Sprintf("key-%08d", n))
			mt.Put(key, v)
		}
	})
}
