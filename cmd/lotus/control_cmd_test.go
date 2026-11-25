package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// socketDir returns a temporary directory with a short path. Unix socket paths
// are limited to 104 bytes on macOS, and its default temp directory is longer
// than t.TempDir() can afford once the test name is added.
func socketDir(t *testing.T) string {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "lt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// controlled is a running server with a control socket in a temp directory.
type controlled struct {
	t       *testing.T
	sock    string
	cfgPath string
	root    string
	port    int
	addr    string
	stderr  *safeBuf
	done    chan int
	cancel  context.CancelFunc
	exited  bool
	exit    int
}

// waitExit waits for the server to stop and returns its exit code. It can be
// called more than once.
func (c *controlled) waitExit() (code int, ok bool) {
	if c.exited {
		return c.exit, true
	}
	select {
	case c.exit = <-c.done:
		c.exited = true
		return c.exit, true
	case <-time.After(10 * time.Second):
		return 0, false
	}
}

func startControlled(t *testing.T) *controlled {
	t.Helper()
	port := freePort(t)
	dir := socketDir(t)
	sock := filepath.Join(dir, "ctl", "control.sock")
	cfgPath, root := siteConfig(t, port, fmt.Sprintf(`    control { socket "%s" }`, filepath.ToSlash(sock)))

	ctx, cancel := context.WithCancel(context.Background())
	c := &controlled{t: t, sock: sock, cfgPath: cfgPath, root: root, port: port, stderr: &safeBuf{}, done: make(chan int, 1), cancel: cancel}
	var stdout safeBuf
	addrs := make(chan map[string]net.Addr, 1)
	go func() {
		c.done <- runServer(ctx, []string{"-config", cfgPath}, &stdout, c.stderr, func(a map[string]net.Addr) { addrs <- a }, nil)
	}()
	select {
	case a := <-addrs:
		c.addr = a["http"].String()
	case code := <-c.done:
		t.Fatalf("exited early with %d:\n%s", code, c.stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		c.waitExit()
	})
	return c
}

// cli runs `lotus <command> --control <socket> <args...>`.
func (c *controlled) cli(args ...string) (int, string, string) {
	all := []string{args[0], "--control", c.sock}
	return runCLI(append(all, args[1:]...)...)
}

func (c *controlled) get(path string) (int, string) {
	req, _ := http.NewRequest(http.MethodGet, "http://"+c.addr+path, nil)
	req.Host = "localhost"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// configText builds a configuration for this server; listenPort lets a test
// change the listener, and siteExtra is added inside the site.
func (c *controlled) configText(listenPort int, siteExtra string) string {
	return fmt.Sprintf(`lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:%d" }
    control { socket "%s" }
    site "localhost" {
        root "%s"
        tls off
%s
    }
}
`, listenPort, filepath.ToSlash(c.sock), filepath.ToSlash(c.root), siteExtra)
}

func (c *controlled) write(path, text string) {
	c.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

const pingRoutes = `route = "/ping" { return 200 "pong" }
route "/" { }`

func TestControlStatus(t *testing.T) {
	c := startControlled(t)
	code, out, errOut := c.cli("status")
	if code != exitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"pid " + fmt.Sprint(os.Getpid()), "generation 1", "sites: 1", "listener http: http on " + c.addr} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = c.cli("status", "--json")
	var st struct {
		PID        int    `json:"pid"`
		Generation uint64 `json:"generation"`
		ConfigFile string `json:"config_file"`
		Sites      int    `json:"sites"`
	}
	if code != exitOK || json.Unmarshal([]byte(out), &st) != nil || st.Generation != 1 || st.Sites != 1 || st.ConfigFile != c.cfgPath {
		t.Errorf("json status: %d %s", code, out)
	}
}

func TestControlReload(t *testing.T) {
	c := startControlled(t)
	if code, _ := c.get("/ping"); code != 404 {
		t.Fatalf("/ping before = %d", code)
	}

	// A valid change takes effect, and the generation counts up.
	c.write(c.cfgPath, c.configText(c.port, pingRoutes))
	code, out, errOut := c.cli("reload")
	if code != exitOK || !strings.Contains(out, "configuration reloaded (generation 2)") {
		t.Fatalf("reload: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if code, body := c.get("/ping"); code != 200 || strings.TrimSpace(body) != "pong" {
		t.Errorf("after reload: %d %q", code, body)
	}

	// An invalid file: exit 2, the diagnostics are printed, the old config stays.
	c.write(c.cfgPath, c.configText(c.port, `bogus_directive 1`))
	code, out, errOut = c.cli("reload")
	if code != exitInvalidConfig || out != "" {
		t.Errorf("invalid reload: code=%d stdout=%q", code, out)
	}
	for _, want := range []string{"LOTUS-CFG-0120", "reload refused"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	if code, _ := c.get("/ping"); code != 200 {
		t.Errorf("a refused reload changed the running configuration: %d", code)
	}

	// A change that needs a restart is refused with the reason.
	c.write(c.cfgPath, c.configText(freePort(t), pingRoutes))
	code, _, errOut = c.cli("reload")
	if code != exitInvalidConfig || !strings.Contains(errOut, "need a restart") {
		t.Errorf("restart-needed reload: code=%d stderr=%q", code, errOut)
	}

	// --config loads another file, and the status then shows it.
	other := filepath.Join(t.TempDir(), "other.conf")
	c.write(other, c.configText(c.port, `route = "/ping" { return 200 "from-other" }`+"\nroute \"/\" { }"))
	if code, out, errOut := c.cli("reload", "--config", other); code != exitOK {
		t.Fatalf("--config: code=%d %q %q", code, out, errOut)
	}
	if _, body := c.get("/ping"); strings.TrimSpace(body) != "from-other" {
		t.Errorf("--config was not loaded: %q", body)
	}
	if _, out, _ = c.cli("status", "--json"); !strings.Contains(out, "other.conf") {
		t.Errorf("the status must show the file now in use:\n%s", out)
	}
}

func TestControlLogsReopen(t *testing.T) {
	c := startControlled(t)
	code, out, _ := c.cli("logs", "reopen")
	if code != exitOK || !strings.Contains(out, "reopened 0 log file(s)") {
		t.Errorf("no log files configured: %d %q", code, out)
	}

	logPath := filepath.Join(t.TempDir(), "access.log")
	c.write(c.cfgPath, c.configText(c.port, fmt.Sprintf(`logs { access "%s" }`, filepath.ToSlash(logPath))))
	if code, _, errOut := c.cli("reload"); code != exitOK {
		t.Fatalf("reload: %d %s", code, errOut)
	}
	code, out, _ = c.cli("logs", "reopen")
	if code != exitOK || !strings.Contains(out, "reopened 1 log file(s)") {
		t.Errorf("%d %q", code, out)
	}
	if code, _, _ := runCLI("logs", "--control", c.sock); code != exitUsage {
		t.Errorf("logs without a subcommand: %d", code)
	}
}

func TestControlStopStopsTheServer(t *testing.T) {
	c := startControlled(t)
	code, out, errOut := c.cli("stop")
	if code != exitOK || !strings.Contains(out, "stopping") {
		t.Fatalf("stop: %d %q %q", code, out, errOut)
	}
	exit, ok := c.waitExit()
	if !ok {
		t.Fatal("the server did not stop")
	}
	if exit != exitOK {
		t.Errorf("the server exited with %d\n%s", exit, c.stderr.String())
	}
	if !strings.Contains(c.stderr.String(), "stop requested through the control channel") {
		t.Errorf("log:\n%s", c.stderr.String())
	}
	if code, _, errOut := c.cli("status"); code != exitUnreachable || !strings.Contains(errOut, "no running server") {
		t.Errorf("after stop: code=%d %q", code, errOut)
	}
	if _, err := os.Lstat(c.sock); err == nil {
		t.Error("the socket file must be gone after a clean stop")
	}
}

func TestControlStopNow(t *testing.T) {
	c := startControlled(t)
	if code, _, errOut := c.cli("stop", "--now"); code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	exit, ok := c.waitExit()
	if !ok {
		t.Fatal("the server did not stop")
	}
	if exit != exitOK {
		t.Errorf("exit code %d\n%s", exit, c.stderr.String())
	}
}

func TestControlUnreachableAndUsage(t *testing.T) {
	sock := filepath.Join(socketDir(t), "none.sock")
	for _, cmd := range []string{"status", "reload", "stop"} {
		code, out, errOut := runCLI(cmd, "--control", sock)
		if code != exitUnreachable || out != "" || !strings.Contains(errOut, sock) {
			t.Errorf("%s: code=%d stdout=%q stderr=%q", cmd, code, out, errOut)
		}
	}
	if code, _, _ := runCLI("status", "extra"); code != exitUsage {
		t.Errorf("an extra argument: %d", code)
	}
	if code, _, _ := runCLI("stop", "--timeout", "nonsense"); code != exitUsage {
		t.Errorf("a bad duration: %d", code)
	}
}

func TestExplicitControlSocketFailureStopsTheStart(t *testing.T) {
	dir := socketDir(t)
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The socket's directory would have to be inside a regular file.
	sock := filepath.Join(blocker, "sub", "control.sock")
	cfgPath, _ := siteConfig(t, freePort(t), fmt.Sprintf(`    control { socket "%s" }`, filepath.ToSlash(sock)))
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, nil)
	if code != exitRuntime || !strings.Contains(stderr.String(), "control channel") {
		t.Errorf("code=%d\n%s", code, stderr.String())
	}
}

func TestSocketAlreadyInUseStopsTheStart(t *testing.T) {
	c := startControlled(t)
	// A second server asked to use the same socket must refuse, not steal it.
	cfgPath, _ := siteConfig(t, freePort(t), fmt.Sprintf(`    control { socket "%s" }`, filepath.ToSlash(c.sock)))
	var stdout, stderr safeBuf
	code := runStart(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, nil)
	if code != exitRuntime || !strings.Contains(stderr.String(), "already listening") {
		t.Errorf("code=%d\n%s", code, stderr.String())
	}
	if code, _, _ := c.cli("status"); code != exitOK {
		t.Errorf("the first server must still answer: %d", code)
	}
}
