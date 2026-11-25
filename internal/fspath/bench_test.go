package fspath

import (
	"os"
	"path/filepath"
	"testing"
)

func benchRoot(b *testing.B) (string, *Resolver) {
	b.Helper()
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), make([]byte, 1024), 0o644); err != nil {
		b.Fatal(err)
	}
	r, err := New(dir, Policy{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { r.Close() })
	return dir, r
}

// BenchmarkOpen is the cost of resolving and opening one small file.
func BenchmarkOpen(b *testing.B) {
	_, r := benchRoot(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f, err := r.Open("/small.txt")
		if err != nil {
			b.Fatal(err)
		}
		f.Close()
	}
}

// BenchmarkPlainOpen is the same without any of the checks, for comparison.
func BenchmarkPlainOpen(b *testing.B) {
	dir, _ := benchRoot(b)
	p := filepath.Join(dir, "small.txt")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f, err := os.Open(p)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = f.Stat()
		f.Close()
	}
}
