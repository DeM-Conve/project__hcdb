package bench

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hchauhan7816/hcdb/cache"
)

func BenchmarkCachePut(b *testing.B) {
	c := cache.NewLRU(1024)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%08d", i)
		c.Put(key, i)
	}
}

func BenchmarkCachePutParallel(b *testing.B) {
	c := cache.NewLRU(1024)

	var counter atomic.Int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			key := fmt.Sprintf("key-%08d", n)
			c.Put(key, n)
		}
	})
}

func BenchmarkCacheGet(b *testing.B) {
	c := cache.NewLRU(1024)
	for i := 0; i < 1024; i++ {
		c.Put(fmt.Sprintf("key-%08d", i), i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%08d", i%1024)
		c.Get(key)
	}
}

func BenchmarkCacheGetParallel(b *testing.B) {
	c := cache.NewLRU(1024)
	for i := 0; i < 1024; i++ {
		c.Put(fmt.Sprintf("key-%08d", i), i)
	}

	var counter atomic.Int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			key := fmt.Sprintf("key-%08d", n%1024)
			c.Get(key)
		}
	})
}
