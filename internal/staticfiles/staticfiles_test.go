package staticfiles

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/fspath"
)

func newSite(t *testing.T, opts Options, policy fspath.Policy) (*httptest.Server, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<h1>home</h1>")
	write("hello.txt", "hello, world")
	write("style.css", "body{}")
	write("data.wasm", "\x00asm")
	write("unknown.zzzqq", "plain bytes here")
	write("docs/index.htm", "<p>docs</p>")
	write("docs/guide.html", "<p>guide</p>")
	write("empty/readme.txt", "x")
	write(".env", "SECRET=1")
	write("secret/.git/config", "[core]")
	if err := os.WriteFile(filepath.Join(base, "outside.txt"), []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := fspath.New(root, policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Close() })
	srv := httptest.NewServer(New(res, opts))
	t.Cleanup(srv.Close)
	return srv, root
}

func get(t *testing.T, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestServesFiles(t *testing.T) {
	srv, _ := newSite(t, Options{Index: []string{"index.html", "index.htm"}}, fspath.Policy{})
	tests := []struct {
		path, body, typePrefix string
	}{
		{"/hello.txt", "hello, world", "text/plain"},
		{"/style.css", "body{}", "text/css"},
		{"/", "<h1>home</h1>", "text/html"},
		{"/index.html", "<h1>home</h1>", "text/html"},
		{"/docs/", "<p>docs</p>", "text/html"}, // second index candidate
		{"/docs/guide.html", "<p>guide</p>", "text/html"},
		{"//docs///guide.html", "<p>guide</p>", "text/html"}, // repeated slashes are merged
	}
	for _, tt := range tests {
		resp, body := get(t, srv.URL+tt.path, nil)
		if resp.StatusCode != 200 || body != tt.body {
			t.Errorf("GET %s = %d %q, want 200 %q", tt.path, resp.StatusCode, body, tt.body)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, tt.typePrefix) {
			t.Errorf("GET %s Content-Type = %q, want prefix %q", tt.path, ct, tt.typePrefix)
		}
		if resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Last-Modified") == "" || resp.Header.Get("Etag") == "" {
			t.Errorf("GET %s missing validators/range headers: %v", tt.path, resp.Header)
		}
	}
}

func TestMIMEOverridesAndSniffing(t *testing.T) {
	srv, _ := newSite(t, Options{MIME: map[string]string{".WASM": "application/wasm"}}, fspath.Policy{})
	resp, _ := get(t, srv.URL+"/data.wasm", nil)
	if ct := resp.Header.Get("Content-Type"); ct != "application/wasm" {
		t.Errorf("override: Content-Type = %q", ct)
	}
	resp, _ = get(t, srv.URL+"/unknown.zzzqq", nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("an unknown extension is sniffed from content; got %q", ct)
	}
}

func TestConditionalRequests(t *testing.T) {
	srv, root := newSite(t, Options{}, fspath.Policy{})
	resp, _ := get(t, srv.URL+"/hello.txt", nil)
	etag, lastMod := resp.Header.Get("Etag"), resp.Header.Get("Last-Modified")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q, want a weak validator", etag)
	}

	resp, body := get(t, srv.URL+"/hello.txt", map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified || body != "" {
		t.Errorf("If-None-Match: %d %q, want 304 and no body", resp.StatusCode, body)
	}
	resp, _ = get(t, srv.URL+"/hello.txt", map[string]string{"If-None-Match": `W/"other"`})
	if resp.StatusCode != 200 {
		t.Errorf("a different ETag must give 200, got %d", resp.StatusCode)
	}
	resp, _ = get(t, srv.URL+"/hello.txt", map[string]string{"If-Modified-Since": lastMod})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("If-Modified-Since: %d, want 304", resp.StatusCode)
	}

	// Changing the file changes the ETag.
	p := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(p, []byte("changed content!"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	os.Chtimes(p, future, future)
	resp, body = get(t, srv.URL+"/hello.txt", map[string]string{"If-None-Match": etag})
	if resp.StatusCode != 200 || body != "changed content!" || resp.Header.Get("Etag") == etag {
		t.Errorf("after a change: %d %q etag=%q", resp.StatusCode, body, resp.Header.Get("Etag"))
	}
}

func TestRangeRequests(t *testing.T) {
	srv, _ := newSite(t, Options{}, fspath.Policy{})
	resp, body := get(t, srv.URL+"/hello.txt", map[string]string{"Range": "bytes=7-11"})
	if resp.StatusCode != http.StatusPartialContent || body != "world" || resp.Header.Get("Content-Range") != "bytes 7-11/12" {
		t.Errorf("range: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	resp, body = get(t, srv.URL+"/hello.txt", map[string]string{"Range": "bytes=-5"})
	if resp.StatusCode != http.StatusPartialContent || body != "world" {
		t.Errorf("suffix range: %d %q", resp.StatusCode, body)
	}
	resp, _ = get(t, srv.URL+"/hello.txt", map[string]string{"Range": "bytes=100-200"})
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("unsatisfiable range: %d, want 416", resp.StatusCode)
	}
}

func TestHeadAndMethods(t *testing.T) {
	srv, _ := newSite(t, Options{}, fspath.Policy{})
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/hello.txt", nil)
	resp, body := do(t, req)
	if resp.StatusCode != 200 || body != "" || resp.Header.Get("Content-Length") != "12" {
		t.Errorf("HEAD: %d body=%q length=%q", resp.StatusCode, body, resp.Header.Get("Content-Length"))
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, "BREW"} {
		req, _ := http.NewRequest(m, srv.URL+"/hello.txt", strings.NewReader("x"))
		resp, _ := do(t, req)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: %d Allow=%q, want 405 with Allow: GET, HEAD", m, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
}

func TestDirectories(t *testing.T) {
	srv, _ := newSite(t, Options{}, fspath.Policy{})

	resp, _ := get(t, srv.URL+"/docs?x=1", nil)
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/docs/?x=1" {
		t.Errorf("directory without slash: %d Location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Only index.html is configured, and docs/ has none, so this is a directory without an index.
	resp, body := get(t, srv.URL+"/docs/", nil)
	if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "guide.html") {
		t.Errorf("directory without index: %d %q; listings must stay off", resp.StatusCode, body)
	}
	resp, _ = get(t, srv.URL+"/empty/", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("directory without index: %d, want 403", resp.StatusCode)
	}
}

func TestRedirectCannotLeaveTheSite(t *testing.T) {
	srv, _ := newSite(t, Options{}, fspath.Policy{})
	// A request path that looks like a protocol-relative URL must not become an open redirect.
	for _, p := range []string{"//docs", "///docs", "//docs//"} {
		resp, _ := get(t, srv.URL+p, nil)
		if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "//") || strings.Contains(loc, "://") {
			t.Errorf("GET %s redirected to %q", p, loc)
		}
	}
}

func TestMissingAndHiddenAndTraversal(t *testing.T) {
	srv, _ := newSite(t, Options{}, fspath.Policy{})
	tests := []struct {
		path string
		want int
	}{
		{"/nope.txt", 404},
		{"/hello.txt/more", 404},
		{"/.env", 403},
		{"/secret/.git/config", 403},
		{"/.ENV", 404}, // a different name on case-sensitive systems; either way not the secret
	}
	for _, tt := range tests {
		resp, body := get(t, srv.URL+tt.path, nil)
		if tt.path == "/.ENV" {
			// On a case-insensitive file system this IS .env, and must still be refused.
			if resp.StatusCode != 404 && resp.StatusCode != 403 {
				t.Errorf("GET %s = %d", tt.path, resp.StatusCode)
			}
		} else if resp.StatusCode != tt.want {
			t.Errorf("GET %s = %d, want %d", tt.path, resp.StatusCode, tt.want)
		}
		if strings.Contains(body, "SECRET") || strings.Contains(body, "OUTSIDE") {
			t.Errorf("GET %s leaked content: %q", tt.path, body)
		}
	}
}

// Raw requests let us send paths that Go's HTTP client would clean up.
func TestTraversalAttempts(t *testing.T) {
	srv, _ := newSite(t, Options{}, fspath.Policy{})
	addr := strings.TrimPrefix(srv.URL, "http://")
	paths := []string{
		"/../outside.txt", "/docs/../../outside.txt", "/%2e%2e/outside.txt", "/docs/%2e%2e/%2e%2e/outside.txt",
		"/..%2foutside.txt", "/%2e%2e%2foutside.txt", "/docs/..%5coutside.txt", "/..%5coutside.txt",
		"/hello.txt%00.png", "/%00", "/docs/%2e%2e/index.html",
	}
	for _, p := range paths {
		status, body := rawGet(t, addr, p)
		if strings.Contains(body, "OUTSIDE") {
			t.Errorf("GET %s leaked a file from outside the root", p)
		}
		if status == 200 {
			t.Errorf("GET %s = 200, want an error", p)
		}
	}
}

// rawGet sends a GET with the path exactly as given (no client-side cleaning).
func rawGet(t *testing.T, addr, rawPath string) (int, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", rawPath)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("GET %s: %v", rawPath, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSymlinkEscapeIsNotServed(t *testing.T) {
	srv, root := newSite(t, Options{}, fspath.Policy{Symlinks: fspath.SymlinksFollow})
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.Symlink(outside, filepath.Join(root, "leak.txt")); err != nil {
		t.Skipf("cannot create symlinks here (%v); the same behavior is tested in internal/fspath on platforms that allow it", err)
	}
	resp, body := get(t, srv.URL+"/leak.txt", nil)
	if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "OUTSIDE") {
		t.Errorf("symlink out of the root: %d %q", resp.StatusCode, body)
	}
}

func TestErrorResponsesDoNotRevealPaths(t *testing.T) {
	srv, root := newSite(t, Options{}, fspath.Policy{})
	for _, p := range []string{"/nope", "/.env", "/docs/"} {
		_, body := get(t, srv.URL+p, nil)
		if strings.Contains(body, root) || strings.Contains(body, filepath.Base(root)+string(filepath.Separator)) {
			t.Errorf("GET %s response reveals a file system path: %q", p, body)
		}
	}
}

func TestLargeFileIsStreamed(t *testing.T) {
	srv, root := newSite(t, Options{}, fspath.Policy{})
	// 8 MiB is far larger than any buffer; the whole body must arrive intact.
	big := strings.Repeat("0123456789abcdef", 512*1024)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, body := get(t, srv.URL+"/big.bin", nil)
	if resp.StatusCode != 200 || body != big {
		t.Errorf("large file: status %d, got %d bytes, want %d", resp.StatusCode, len(body), len(big))
	}
	resp, body = get(t, srv.URL+"/big.bin", map[string]string{"Range": "bytes=4194304-4194319"})
	if resp.StatusCode != http.StatusPartialContent || body != "0123456789abcdef" {
		t.Errorf("range in a large file: %d %q", resp.StatusCode, body)
	}
}

// Describe must predict exactly what ServeHTTP does: they share one decision.
func TestDescribeMatchesServeHTTP(t *testing.T) {
	srv, _ := newSite(t, Options{Index: []string{"index.html", "index.htm"}}, fspath.Policy{})
	h := srv.Config.Handler.(*Handler)

	for _, method := range []string{"GET", "HEAD", "POST", "DELETE"} {
		for _, p := range []string{
			"/", "/hello.txt", "/style.css", "/nope", "/.env", "/secret/.git/config",
			"/docs", "/docs/", "/docs/guide.html", "/empty", "/empty/", "/empty/readme.txt",
			"/hello.txt/", "/a/../hello.txt",
		} {
			want := h.Describe(method, p)

			req := httptest.NewRequest(method, "http://x"+p, nil)
			req.URL.Path = p // httptest cleans nothing, but be explicit
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != want.Status {
				t.Errorf("%s %s: served %d, Describe says %d", method, p, rec.Code, want.Status)
			}
			if want.Redirect != "" && rec.Header().Get("Location") != want.Redirect {
				t.Errorf("%s %s: Location %q, Describe says %q", method, p, rec.Header().Get("Location"), want.Redirect)
			}
			if want.Status == 200 && method == "GET" && rec.Body.Len() == 0 {
				t.Errorf("%s %s: Describe says file %q but the body is empty", method, p, want.File)
			}
		}
	}
	if got := h.Describe("GET", "/"); got.File != "/index.html" {
		t.Errorf("index file = %q", got.File)
	}
	if got := h.Describe("GET", "/docs/"); got.File != "/docs/index.htm" {
		t.Errorf("second index file = %q", got.File)
	}
}
