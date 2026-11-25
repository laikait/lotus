package staticfiles

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/laikait/lotus/internal/fspath"
)

func benchServe(b *testing.B, h http.Handler) {
	b.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	tr := &http.Transport{MaxIdleConnsPerHost: 64}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr}
	b.ReportAllocs()
	b.SetParallelism(4)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := c.Get(srv.URL + "/small.txt")
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	})
}

// BenchmarkHandler serves a 1 KiB file through the static handler over a real
// connection; BenchmarkPlainFileServer is the standard library's handler on the
// same file, to see how much of the cost is ours.
func BenchmarkHandler(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), make([]byte, 1024), 0o644); err != nil {
		b.Fatal(err)
	}
	res, err := fspath.New(dir, fspath.Policy{})
	if err != nil {
		b.Fatal(err)
	}
	defer res.Close()
	benchServe(b, New(res, Options{}))
}

func BenchmarkPlainFileServer(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), make([]byte, 1024), 0o644); err != nil {
		b.Fatal(err)
	}
	benchServe(b, http.FileServer(http.Dir(dir)))
}
