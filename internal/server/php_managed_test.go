package server

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

// managedConfig is a PHP site whose backend is a pool of processes LOTUS runs.
// The processes are this test binary acting as php-cgi.
func managedConfig(t *testing.T, processes, maxRequests int) *model.Config {
	t.Helper()
	t.Setenv("PHPRC", fcgitest.ChildPHPRC) // the supervisor passes PHPRC on; it turns the child into a fake php-cgi
	exe, err := os.Executable()
	must(t, err)
	cfg := routedConfig(t, func(s *model.Site) {
		s.Index = []string{"index.php", "index.html"}
		writePHPFiles(t, s.Root)
	}, phpRoutes())
	cfg.PHPBackends = []model.PHPBackend{{
		Name: "app", Command: exe, Processes: processes, MaxRequests: maxRequests,
		Timeout: model.Duration(5 * time.Second),
	}}
	return cfg
}

// sameSite points cfg at the document root of other, so the two can be
// reloaded into each other (each managedConfig makes a new temporary root).
func sameSite(cfg, other *model.Config) {
	root := other.Sites[0].Root
	cfg.Sites[0].Root = root
	for i := range cfg.Sites[0].Routes {
		cfg.Sites[0].Routes[i].Root = root
	}
}

// pid runs /app.php and returns the id of the process that answered.
func pid(t *testing.T, base string) int {
	t.Helper()
	resp, body := do(t, "GET", base+"/app.php", nil, "")
	n, err := strconv.Atoi(strings.TrimSpace(body))
	if resp.StatusCode != 200 || err != nil {
		t.Fatalf("GET /app.php = %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Concurrent"); got != "1" {
		t.Errorf("X-Concurrent = %q: a php-cgi process was given two requests at once", got)
	}
	return n
}

func poolCount(s *Server) int {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	return len(s.pools)
}

func TestManagedPHPRunsScripts(t *testing.T) {
	h := start(t, managedConfig(t, 2, 0))
	base := "http://" + h.addr("http")
	seen := map[int]bool{}
	for i := 0; i < 12; i++ {
		seen[pid(t, base)] = true
	}
	if len(seen) == 0 || seen[os.Getpid()] {
		t.Errorf("scripts must run in the php-cgi processes, not in the server: %v", seen)
	}
	// The usual routes work on top of it: front controller and static files.
	if resp, _ := do(t, "GET", base+"/pretty/url", nil, ""); resp.StatusCode != 200 {
		t.Errorf("front controller: %d", resp.StatusCode)
	}
	if resp, body := do(t, "GET", base+"/style.css", nil, ""); resp.StatusCode != 200 || body != "body{}" {
		t.Errorf("static file: %d %q", resp.StatusCode, body)
	}
}

func TestManagedPHPRecyclesProcessesWithoutErrors(t *testing.T) {
	h := start(t, managedConfig(t, 2, 3))
	base := "http://" + h.addr("http")
	seen := map[int]bool{}
	for i := 0; i < 30; i++ {
		seen[pid(t, base)] = true
	}
	if len(seen) < 8 {
		t.Errorf("30 requests ran in %d processes; with max_requests 3 and 2 workers that is at least 10", len(seen))
	}
}

func TestManagedPHPPoolIsKeptAcrossReloads(t *testing.T) {
	cfg := managedConfig(t, 1, 0)
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	first := pid(t, base)

	// The same settings: same processes, no restart, no second pool.
	for i := 0; i < 2; i++ {
		must(t, h.srv.Reload(cfg))
		if got := pid(t, base); got != first {
			t.Fatalf("reload %d restarted the PHP process (%d -> %d)", i+1, first, got)
		}
		if n := poolCount(h.srv); n != 1 {
			t.Fatalf("%d pools after reload", n)
		}
	}

	// Different settings: a new pool takes over and the old one is stopped.
	cfg2 := managedConfig(t, 2, 0)
	cfg2.PHPBackends[0].Command = cfg.PHPBackends[0].Command
	sameSite(cfg2, cfg)
	must(t, h.srv.Reload(cfg2))
	if got := pid(t, base); got == first {
		t.Error("a changed pool size did not start new processes")
	}
	deadline := time.Now().Add(5 * time.Second)
	for poolCount(h.srv) != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := poolCount(h.srv); n != 1 {
		t.Errorf("%d pools: the old one was not stopped", n)
	}
}

func TestManagedPHPStopsWithTheServer(t *testing.T) {
	h := start(t, managedConfig(t, 3, 0))
	pid(t, "http://"+h.addr("http"))
	h.srv.poolMu.Lock()
	var pools []*sharedPool
	for _, sp := range h.srv.pools {
		pools = append(pools, sp)
	}
	h.srv.poolMu.Unlock()
	if len(pools) != 1 || pools[0].pool.Alive() != 3 {
		t.Fatalf("pools: %d", len(pools))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, h.srv.Shutdown(ctx))
	if n := pools[0].pool.Alive(); n != 0 {
		t.Errorf("%d php-cgi processes still run after shutdown", n)
	}
	if n := poolCount(h.srv); n != 0 {
		t.Errorf("%d pools left", n)
	}
}

func TestManagedPHPThatCannotStartStopsTheServer(t *testing.T) {
	cfg := managedConfig(t, 1, 0)
	cfg.PHPBackends[0].Command = "no-such-php-cgi-anywhere"
	_, err := New(cfg, Options{})
	if err == nil || !strings.Contains(err.Error(), `php_backend "app"`) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v", err)
	}
}

func TestAFailedReloadKeepsTheRunningPool(t *testing.T) {
	cfg := managedConfig(t, 1, 0)
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	first := pid(t, base)

	bad := managedConfig(t, 2, 0)
	sameSite(bad, cfg)
	bad.PHPBackends[0].Command = "no-such-php-cgi-anywhere"
	if err := h.srv.Reload(bad); err == nil {
		t.Fatal("a reload with an unstartable backend succeeded")
	}
	if got := pid(t, base); got != first {
		t.Error("the failed reload disturbed the running process")
	}
	if n := poolCount(h.srv); n != 1 {
		t.Errorf("%d pools", n)
	}
}

func TestManagedPHPUnderConcurrentLoad(t *testing.T) {
	h := start(t, managedConfig(t, 3, 5))
	base := "http://" + h.addr("http")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				resp, body := do(t, "GET", base+"/app.php", nil, "")
				if resp.StatusCode != 200 || resp.Header.Get("X-Concurrent") != "1" {
					t.Errorf("%d %q concurrent=%q", resp.StatusCode, body, resp.Header.Get("X-Concurrent"))
					return
				}
			}
		}()
	}
	wg.Wait()
}
