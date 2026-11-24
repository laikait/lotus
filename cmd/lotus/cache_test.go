package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/control"
)

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 1: "1 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB",
		1<<20 - 1: "1024.0 KiB", 1 << 20: "1.0 MiB", 64 << 20: "64.0 MiB", 3 << 30: "3.0 GiB", 5 << 40: "5.0 TiB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestWriteCacheStatus(t *testing.T) {
	zones := []control.CacheZone{
		{Zone: "pages", Entries: 1204, Bytes: 40 << 20, MaxBytes: 256 << 20, Hits: 931, Misses: 69, Stale: 2, Bypass: 40, Evictions: 7},
		{Zone: "idle", MaxBytes: 64 << 20},
	}
	var out bytes.Buffer
	writeCacheStatus(&out, zones, false)
	for _, re := range []string{
		`(?m)^ZONE\s+ENTRIES\s+SIZE\s+LIMIT\s+HIT RATE\s+HITS\s+MISSES\s+STALE\s+BYPASS\s+EVICTED$`,
		`(?m)^pages\s+1204\s+40\.0 MiB\s+256\.0 MiB\s+93\.1%\s+931\s+69\s+2\s+40\s+7$`,
		`(?m)^idle\s+0\s+0 B\s+64\.0 MiB\s+-\s+0\s+0\s+0\s+0\s+0$`,
	} {
		if !regexp.MustCompile(re).MatchString(out.String()) {
			t.Errorf("no line matches %s in\n%s", re, out.String())
		}
	}

	out.Reset()
	writeCacheStatus(&out, zones, true)
	var rows []struct {
		Zone    string   `json:"zone"`
		Hits    uint64   `json:"hits"`
		HitRate *float64 `json:"hit_rate_percent"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if rows[0].HitRate == nil || *rows[0].HitRate < 93.0 || *rows[0].HitRate > 93.2 || rows[1].HitRate != nil || rows[0].Hits != 931 {
		t.Errorf("%+v", rows)
	}

	out.Reset()
	writeCacheStatus(&out, nil, true)
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("no zones in JSON: %q", out.String())
	}
	out.Reset()
	writeCacheStatus(&out, nil, false)
	if !strings.Contains(out.String(), "no cache zones") {
		t.Errorf("%q", out.String())
	}
}

func TestHitRateIgnoresBypassedRequests(t *testing.T) {
	if r, ok := hitRate(control.CacheZone{Hits: 1, Misses: 1, Bypass: 1000}); !ok || r != 50 {
		t.Errorf("%v %v", r, ok)
	}
	if _, ok := hitRate(control.CacheZone{Bypass: 5}); ok {
		t.Error("a zone nobody could use has no hit rate")
	}
}

func TestCacheCommandArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand":      {"cache"},
		"unknown subcommand": {"cache", "flush"},
		"purge nothing":      {"cache", "purge"},
		"purge two targets":  {"cache", "purge", "--all", "--path", "/x"},
		"path and prefix":    {"cache", "purge", "--path", "/a", "--prefix", "/b"},
		"all with host":      {"cache", "purge", "--all", "--host", "a.test"},
		"relative path":      {"cache", "purge", "--path", "x"},
		"relative prefix":    {"cache", "purge", "--prefix", "x/"},
		"stray argument":     {"cache", "purge", "--all", "extra"},
		"status argument":    {"cache", "status", "extra"},
	} {
		if code, _, errOut := runCLI(args...); code != exitUsage || errOut == "" {
			t.Errorf("%s: code=%d stderr=%q", name, code, errOut)
		}
	}
	if code, _, errOut := runCLI("cache", "status", "--control", filepath.Join(t.TempDir(), "nobody.sock")); code != exitUnreachable || !strings.Contains(errOut, "no running server") {
		t.Errorf("no server: code=%d %q", code, errOut)
	}
}

// cacheServer starts a server with a cached proxy route to a backend that
// counts its requests, and returns the control socket, the HTTP address and
// the counter.
func cacheServer(t *testing.T, zones string) (sock, addr string, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "page "+r.URL.Path)
	}))
	t.Cleanup(be.Close)

	dir := t.TempDir()
	root := filepath.Join(dir, "www")
	os.MkdirAll(root, 0o755)
	sock = filepath.Join(socketDir(t), "ctl", "control.sock")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	listen := l.Addr().String()
	l.Close()
	cfg := fmt.Sprintf(`lotus_config_version 1
server {
    listen "http" { address "%s" }
    control { socket "%s" }
    upstream "app" { backend "%s" }
    %s
    site "a.test" {
        root "%s"
        route "/app/" {
            proxy "app"
            cache "pages" { ttl 1m }
        }
    }
}
`, listen, filepath.ToSlash(sock), be.URL, zones, filepath.ToSlash(root))
	cfgPath := filepath.Join(dir, "main.lotus")
	os.WriteFile(cfgPath, []byte(cfg), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	ready := make(chan struct{})
	var stdout, stderr safeBuf
	go func() {
		done <- runServer(ctx, []string{"-config", cfgPath}, &stdout, &stderr, func(map[string]net.Addr) { close(ready) }, nil)
	}()
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("exited early with %d:\n%s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	return sock, listen, calls
}

func fetch(t *testing.T, addr, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	req.Host = "a.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("X-Cache")
}

func TestCacheCommandsAgainstARunningServer(t *testing.T) {
	sock, addr, calls := cacheServer(t, `cache "pages" { max_size 8mib }`)
	for _, p := range []string{"/app/news/1", "/app/news/2", "/app/other", "/app/news/1", "/app/news/1"} {
		fetch(t, addr, p)
	}
	if calls.Load() != 3 {
		t.Fatalf("backend saw %d requests", calls.Load())
	}

	code, out, errOut := runCLI("cache", "status", "--control", sock)
	if code != exitOK || errOut != "" {
		t.Fatalf("status: code=%d stderr=%q\n%s", code, errOut, out)
	}
	if !regexp.MustCompile(`(?m)^pages\s+3\s+\S+ \S+\s+8\.0 MiB\s+40\.0%\s+2\s+3\s+0\s+0\s+0$`).MatchString(out) {
		t.Errorf("status output:\n%s", out)
	}
	code, out, _ = runCLI("cache", "status", "--control", sock, "--json", "--zone", "pages")
	var rows []struct {
		Zone    string `json:"zone"`
		Entries int    `json:"entries"`
		Hits    int    `json:"hits"`
	}
	if code != exitOK || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Entries != 3 || rows[0].Hits != 2 {
		t.Errorf("json: %d %s", code, out)
	}
	if code, _, errOut := runCLI("cache", "status", "--control", sock, "--zone", "nope"); code != exitNotFound || !strings.Contains(errOut, "no such cache zone") {
		t.Errorf("unknown zone: %d %q", code, errOut)
	}

	// A prefix purge removes the pages below it, and the next request goes to the backend.
	code, out, errOut = runCLI("cache", "purge", "--control", sock, "--prefix", "/app/news/", "--host", "a.test")
	if code != exitOK || strings.TrimSpace(out) != "purged 2 entries" || errOut != "" {
		t.Fatalf("purge prefix: %d %q %q", code, out, errOut)
	}
	if got := fetch(t, addr, "/app/news/1"); got != "MISS" {
		t.Errorf("after the purge: %q", got)
	}
	if got := fetch(t, addr, "/app/other"); got != "HIT" {
		t.Errorf("a page outside the prefix was removed: %q", got)
	}
	code, out, _ = runCLI("cache", "purge", "--control", sock, "--path", "/app/other", "--zone", "pages")
	if code != exitOK || strings.TrimSpace(out) != "purged 1 entry" {
		t.Errorf("purge path: %d %q", code, out)
	}
	code, out, _ = runCLI("cache", "purge", "--control", sock, "--all")
	if code != exitOK || strings.TrimSpace(out) != "purged 1 entry" {
		t.Errorf("purge all: %d %q", code, out)
	}
	if code, out, _ := runCLI("cache", "purge", "--control", sock, "--all"); code != exitOK || strings.TrimSpace(out) != "purged 0 entries" {
		t.Errorf("purge of an empty zone: %d %q", code, out)
	}
	if code, _, errOut := runCLI("cache", "purge", "--control", sock, "--all", "--zone", "nope"); code != exitNotFound || !strings.Contains(errOut, "no such cache zone") {
		t.Errorf("purge of an unknown zone: %d %q", code, errOut)
	}
}

func TestCachePurgeNeedsAZoneWhenThereAreSeveral(t *testing.T) {
	sock, _, _ := cacheServer(t, "cache \"pages\"\n    cache \"other\"")
	code, _, errOut := runCLI("cache", "purge", "--control", sock, "--all")
	if code != exitUsage || !strings.Contains(errOut, "name the zone") || !strings.Contains(errOut, "other, pages") {
		t.Errorf("code=%d %q", code, errOut)
	}
	if code, out, _ := runCLI("cache", "purge", "--control", sock, "--all", "--zone", "other"); code != exitOK || !strings.Contains(out, "purged 0") {
		t.Errorf("%d %q", code, out)
	}
	code, out, _ := runCLI("cache", "status", "--control", sock)
	if code != exitOK || !strings.Contains(out, "pages") || !strings.Contains(out, "other") {
		t.Errorf("status of every zone: %d %q", code, out)
	}
}
