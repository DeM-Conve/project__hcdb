package bench

import (
	"path/filepath"
	"testing"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/wal"
)

func BenchmarkWalAppend(b *testing.B) {

	dir := b.TempDir()
	walObj, err := wal.Open(filepath.Join(dir, "main.wal"))
	if err != nil {
		b.Fatalf("open wal: %v", err)
	}

	k := "RandomTestingKey"
	v := "RandomValueKeyCreatedForTesting"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		entry := wal.Entry{Type: config.OP_PUT, Key: []byte(k), Value: []byte(v)}
		err := walObj.Append(entry)
		if err != nil {
			b.Fatalf("append: %v", err)
		}
	}

}
