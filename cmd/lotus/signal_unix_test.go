//go:build unix

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This test sends real signals to its own process: SIGHUP must reload the
// configuration, and SIGTERM must stop the server cleanly.
func TestSIGHUPReloadsAndSIGTERMStops(t *testing.T) {
	port := freePort(t)
	cfgPath, root := siteConfig(t, port, "")

	var stdout, stderr safeBuf
	done := make(chan int, 1)
	go func() { done <- cmdStart([]string{"-config", cfgPath}, &stdout, &stderr) }()

	url := "http://127.0.0.1:" + fmt.Sprint(port)
	get := func(path string) (int, string, bool) {
		req, _ := http.NewRequest(http.MethodGet, url+path, nil)
		req.Host = "localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), true
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s\n%s", what, stderr.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	waitFor("the server to answer", func() bool { code, _, up := get("/"); return up && code == 200 })
	if code, _, _ := get("/ping"); code != 404 {
		t.Fatalf("/ping before = %d", code)
	}

	if err := os.WriteFile(cfgPath, []byte(reloadConfigText(port, root, `route = "/ping" { return 200 "pong" }`+"\n"+`route "/" { }`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor("the reload to take effect", func() bool { code, body, _ := get("/ping"); return code == 200 && strings.TrimSpace(body) == "pong" })

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code %d\n%s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not stop the server")
	}
	for _, want := range []string{"msg=\"reloading configuration\"", "msg=\"configuration reloaded\"", "msg=\"shutting down\"", "msg=stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, stderr.String())
		}
	}
}
