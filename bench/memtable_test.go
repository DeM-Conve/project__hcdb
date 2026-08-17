package bench

import (
	"fmt"
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
