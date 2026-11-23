package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"
)

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// freePort returns a port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// siteConfig writes a document root and a configuration for one site.
func siteConfig(t *testing.T, port int, extra string) (cfgPath, root string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "www")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("hello from lotus"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Forward slashes keep the path valid inside a quoted string on Windows.
	cfg := fmt.Sprintf(`lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:%d" }
    site "localhost" {
        root "%s"
        tls off
    }
%s
}
`, port, filepath.ToSlash(root), extra)
	cfgPath = filepath.Join(dir, "main.lotus")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, root
}

func TestStartServesAndStopsGracefully(t *testing.T) {
	port := freePort(t)
	cfgPath, _ := siteConfig(t, port, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr safeBuf
	addrs := make(chan map[string]net.Addr, 1)
	done := make(chan int, 1)
	go func() {
		done <- runStart(ctx, []string{"-config", cfgPath}, &stdout, &stderr, func(a map[string]net.Addr) { addrs <- a })
	}()

	var addr string
	select {
	case a := <-addrs:
		addr = a["http"].String()
	case code := <-done:
		t.Fatalf("exited early with %d:\n%s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not become ready")
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	req.Host = "localhost"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello from lotus" {
		t.Errorf("%d %q", resp.StatusCode, body)
	}

	cancel() // what SIGINT or SIGTERM does
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code = %d, want 0\n%s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
	for _, want := range []string{"msg=started", "msg=listening", "msg=\"shutting down\"", "msg=stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("server log lacks %q:\n%s", want, stderr.String())
		}
	}
	if !strings.Contains(stdout.String(), `"GET / HTTP/1.1" 200 16 `) {
		t.Errorf("the access log goes to stdout by default:\n%s", stdout.String())
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("the port must be closed after shutdown")
	}
}

func TestStartRefusesAnInvalidConfiguration(t *testing.T) {
	cfgPath, _ := siteConfig(t, freePort(t), "    bogus_directive 1")
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, nil)
	if code != exitInvalidConfig {
		t.Errorf("exit code = %d, want %d", code, exitInvalidConfig)
	}
	if !strings.Contains(stderr.String(), "LOTUS-CFG-0120") || !strings.Contains(stderr.String(), "not starting") {
		t.Errorf("stderr:\n%s", stderr.String())
	}
}

func TestStartRefusesUnsupportedFeatures(t *testing.T) {
	cfgPath, _ := siteConfig(t, freePort(t), `    control { socket "/tmp/lotus-test.sock" }`)
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, nil)
	if code != exitNotSupported {
		t.Errorf("exit code = %d, want %d\n%s", code, exitNotSupported, stderr.String())
	}
	if !strings.Contains(stderr.String(), "control") {
		t.Errorf("the message must name the feature:\n%s", stderr.String())
	}
}

func TestStartReportsAPortInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	cfgPath, _ := siteConfig(t, taken.Addr().(*net.TCPAddr).Port, "")
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, nil)
	if code != exitRuntime || !strings.Contains(stderr.String(), `listener "http"`) {
		t.Errorf("code=%d stderr:\n%s", code, stderr.String())
	}
}

func TestStartWithMissingConfiguration(t *testing.T) {
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", filepath.Join(t.TempDir(), "nope.lotus")}, &stdout, &stderr, nil)
	if code != exitInvalidConfig || !strings.Contains(stderr.String(), "cannot read configuration file") {
		t.Errorf("code=%d stderr:\n%s", code, stderr.String())
	}
}

func TestStartUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"extra-arg"}, {"-nope"}} {
		var stdout, stderr safeBuf
		if code := runStart(context.Background(), args, &stdout, &stderr, nil); code != exitUsage {
			t.Errorf("%v: exit code = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestDefaultConfigPath(t *testing.T) {
	p := defaultConfigPath()
	if p == "" || !strings.HasSuffix(filepath.ToSlash(p), "main.lotus") {
		t.Errorf("defaultConfigPath() = %q", p)
	}
}

// reloadConfigText builds a configuration like siteConfig's, with extra lines
// inside the site.
func reloadConfigText(port int, root, siteExtra string) string {
	return fmt.Sprintf(`lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:%d" }
    site "localhost" {
        root "%s"
        tls off
%s
    }
}
`, port, filepath.ToSlash(root), siteExtra)
}

func TestReloadOnRequest(t *testing.T) {
	port := freePort(t)
	cfgPath, root := siteConfig(t, port, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr safeBuf
	addrs := make(chan map[string]net.Addr, 1)
	done := make(chan int, 1)
	reloads := make(chan struct{}, 1)
	go func() {
		done <- runServer(ctx, []string{"-config", cfgPath}, &stdout, &stderr, func(a map[string]net.Addr) { addrs <- a }, reloads)
	}()
	var addr string
	select {
	case a := <-addrs:
		addr = a["http"].String()
	case code := <-done:
		t.Fatalf("exited early with %d:\n%s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not become ready")
	}

	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
		req.Host = "localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// reload asks for a reload and waits until the server logged its outcome.
	reload := func(wantLog string) {
		t.Helper()
		before := strings.Count(stderr.String(), wantLog)
		reloads <- struct{}{}
		deadline := time.Now().Add(5 * time.Second)
		for strings.Count(stderr.String(), wantLog) == before {
			if time.Now().After(deadline) {
				t.Fatalf("no %q in the log after a reload request:\n%s", wantLog, stderr.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if code, _ := get("/ping"); code != 404 {
		t.Fatalf("/ping before the reload = %d", code)
	}

	// 1. A valid change takes effect.
	write(reloadConfigText(port, root, `route = "/ping" { return 200 "pong" }`+"\n"+`route "/" { }`))
	reload("msg=\"configuration reloaded\"")
	if code, body := get("/ping"); code != 200 || strings.TrimSpace(body) != "pong" {
		t.Errorf("after a valid reload: %d %q", code, body)
	}

	// 2. An invalid file is refused; the running configuration stays.
	write(reloadConfigText(port, root, `bogus_directive 1`))
	reload("reload refused")
	if !strings.Contains(stderr.String(), "LOTUS-CFG-0120") {
		t.Errorf("the problem must be shown:\n%s", stderr.String())
	}
	if code, body := get("/ping"); code != 200 || strings.TrimSpace(body) != "pong" {
		t.Errorf("after an invalid reload the old configuration must serve: %d %q", code, body)
	}

	// 3. A change that needs a restart is refused with the reason.
	write(reloadConfigText(freePort(t), root, `route = "/ping" { return 200 "pong" }`))
	reload("reload failed")
	if !strings.Contains(stderr.String(), "need a restart") {
		t.Errorf("the log must say a restart is needed:\n%s", stderr.String())
	}
	if code, _ := get("/ping"); code != 200 {
		t.Errorf("after a refused reload: %d", code)
	}

	// 4. A file that can't be read at all is refused too.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	reload("reload refused")
	if code, _ := get("/ping"); code != 200 {
		t.Errorf("after a missing config file: %d", code)
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code %d\n%s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
}

func TestRuntimeSettingsReturnToDefaults(t *testing.T) {
	rt := newRuntimeSettings()
	def := runtime.GOMAXPROCS(0)
	defer rt.apply(0, 0)

	rt.apply(1, 512<<20)
	if got := runtime.GOMAXPROCS(0); got != 1 {
		t.Errorf("GOMAXPROCS = %d", got)
	}
	if got := debug.SetMemoryLimit(-1); got != 512<<20 {
		t.Errorf("memory limit = %d", got)
	}
	rt.apply(0, 0) // the settings were removed from the configuration
	if got := runtime.GOMAXPROCS(0); got != def {
		t.Errorf("GOMAXPROCS = %d, want the default %d", got, def)
	}
	if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
		t.Errorf("memory limit = %d, want none", got)
	}
}
