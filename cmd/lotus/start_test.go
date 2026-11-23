package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	cfgPath, _ := siteConfig(t, freePort(t), `    trusted_proxies [ "10.0.0.0/8" ]`)
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, nil)
	if code != exitNotSupported {
		t.Errorf("exit code = %d, want %d\n%s", code, exitNotSupported, stderr.String())
	}
	if !strings.Contains(stderr.String(), "trusted_proxies") {
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
