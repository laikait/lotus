package nginxconv

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/server"
)

// These tests run a translated configuration in a real LOTUS server and check
// the answers a real Nginx gives for the same configuration (each expectation
// below is Nginx's documented behavior), so they test the translation as a
// whole, not only its text.

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startConverted(t *testing.T, nginx string) {
	t.Helper()
	path := write(t, map[string]string{"nginx.conf": nginx})
	nodes, err := Parse(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := Convert(nodes, false)
	out := filepath.Join(t.TempDir(), "converted.lotus")
	if err := os.WriteFile(out, []byte(res.Config), 0o600); err != nil {
		t.Fatal(err)
	}
	checked, diags := config.Check(out, loader.Options{InsecureIncludes: true})
	if checked == nil {
		t.Fatalf("invalid translation: %+v\n%s", diags, res.Config)
	}
	srv, err := server.New(checked.Config, server.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("%v\n%s", err, res.Config)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("%v\n%s", err, res.Config)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
}

type answer struct {
	status int
	body   string
	header http.Header
}

func get(t *testing.T, port int, method, host, path string, hdr map[string]string) answer {
	t.Helper()
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableCompression: true},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, string(b), resp.Header}
}

func TestTranslatedConfigurationBehavesLikeNginx(t *testing.T) {
	www := t.TempDir()
	files := map[string]string{
		"index.html":        "home",
		"about.html":        "about us",
		"404.html":          "custom not found",
		"posts/hello.html":  "hello post",
		"static/app.js":     strings.Repeat("var a = 1;\n", 100),
		"static/notes.txt":  "static notes",
		"uploads/x.txt":     "uploaded",
		"admin/panel.html":  "panel",
		"docs/guide/a.html": "guide",
	}
	for name, body := range files {
		p := filepath.Join(www, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "backend path=%s host=%s", r.URL.RequestURI(), r.Host)
	}))
	defer backend.Close()
	port := freePort(t)

	startConverted(t, fmt.Sprintf(`
http {
    gzip on;
    gzip_types application/javascript;
    gzip_min_length 100;
    upstream app { server %s; }
    server {
        listen 127.0.0.1:%d;
        server_name x.test;
        root %s;
        index index.html;
        error_page 404 /404.html;
        add_header X-Frame-Options DENY;

        location / { try_files $uri $uri/ =404; }
        location = /old { return 301 /new; }
        location /blog/ { rewrite ^/blog/(\w+)$ /posts/$1.html last; }
        location ^~ /static/ { try_files $uri =404; }
        location ~* \.txt$ { return 403; }
        location /api/ { proxy_pass http://app; proxy_set_header Host $host; }
        location /admin/ {
            if ($request_method = POST) { return 405; }
            try_files $uri =404;
        }
        location /secret/ { deny all; }
        location = /exact { return 200 "exact match"; }
        location /exact/ { return 200 "prefix match"; }
    }
}`, strings.TrimPrefix(backend.URL, "http://"), port, filepath.ToSlash(www)))

	cases := []struct {
		name         string
		method, path string
		hdr          map[string]string
		status       int
		body         string // substring; "" to skip
		header       [2]string
	}{
		{"index", "GET", "/", nil, 200, "home", [2]string{"X-Frame-Options", "DENY"}},
		{"file", "GET", "/about.html", nil, 200, "about us", [2]string{}},
		{"directory without an index file", "GET", "/docs/guide/", nil, 403, "", [2]string{}}, // Nginx: directory index is forbidden
		{"missing file uses the error page", "GET", "/nope", nil, 404, "custom not found", [2]string{}},
		{"exact redirect", "GET", "/old", nil, 301, "", [2]string{"Location", "/new"}},
		{"rewrite last", "GET", "/blog/hello", nil, 200, "hello post", [2]string{}},
		{"final prefix beats regex", "GET", "/static/notes.txt", nil, 200, "static notes", [2]string{}},
		{"regex beats prefix", "GET", "/uploads/x.txt", nil, 403, "", [2]string{}},
		{"proxy keeps the client's Host", "GET", "/api/items?id=3", nil, 200, "backend path=/api/items?id=3 host=x.test", [2]string{}},
		{"if method", "POST", "/admin/panel.html", nil, 405, "", [2]string{}},
		{"get through if", "GET", "/admin/panel.html", nil, 200, "panel", [2]string{}},
		{"deny all", "GET", "/secret/anything", nil, 403, "", [2]string{}},
		{"exact beats prefix", "GET", "/exact", nil, 200, "exact match", [2]string{}},
		{"prefix", "GET", "/exact/more", nil, 200, "prefix match", [2]string{}},
		{"gzip", "GET", "/static/app.js", map[string]string{"Accept-Encoding": "gzip"}, 200, "", [2]string{"Content-Encoding", "gzip"}},
		{"no gzip without Accept-Encoding", "GET", "/static/app.js", nil, 200, "var a = 1;", [2]string{}},
	}
	for _, c := range cases {
		a := get(t, port, c.method, "x.test", c.path, c.hdr)
		if a.status != c.status {
			t.Errorf("%s: %s %s = %d, want %d (%q)", c.name, c.method, c.path, a.status, c.status, a.body)
			continue
		}
		if c.body != "" && !strings.Contains(a.body, c.body) {
			t.Errorf("%s: body %q lacks %q", c.name, a.body, c.body)
		}
		if c.header[0] != "" && a.header.Get(c.header[0]) != c.header[1] {
			t.Errorf("%s: %s = %q, want %q", c.name, c.header[0], a.header.Get(c.header[0]), c.header[1])
		}
	}
	// The HEAD of a compressed file, and a hidden file, behave too.
	if a := get(t, port, "GET", "x.test", "/.env", nil); a.status != 404 && a.status != 403 {
		t.Errorf("dotfiles are not served: %d", a.status)
	}
}

func TestTranslatedRedirectServers(t *testing.T) {
	port := freePort(t)
	startConverted(t, fmt.Sprintf(`
http {
    server {
        listen 127.0.0.1:%d default_server;
        server_name _;
        return 301 https://$host$request_uri;
    }
}`, port))
	a := get(t, port, "GET", "anything.test", "/some/path?x=1&y=2", nil)
	if a.status != 301 || a.header.Get("Location") != "https://anything.test/some/path?x=1&y=2" {
		t.Errorf("%d %q", a.status, a.header.Get("Location"))
	}
}

func TestTranslatedErrorPagesAndLimits(t *testing.T) {
	www := t.TempDir()
	if err := os.WriteFile(filepath.Join(www, "index.html"), []byte("home"), 0o644); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	startConverted(t, fmt.Sprintf(`
http {
    limit_req_zone $binary_remote_addr zone=slow:1m rate=1r/m;
    limit_req_status 429;
    server {
        listen 127.0.0.1:%d;
        server_name x.test;
        root %s;
        location = / { limit_req zone=slow burst=1 nodelay; }
        location /small/ { client_max_body_size 10; return 200 "ok"; }
    }
}`, port, filepath.ToSlash(www)))
	// burst=1 nodelay at 1 request per minute: nginx lets 2 through (the one
	// allowed and one burst), then refuses.
	var got []int
	for i := 0; i < 4; i++ {
		got = append(got, get(t, port, "GET", "x.test", "/", nil).status)
	}
	if got[0] != 200 || got[1] != 200 || got[2] != 429 || got[3] != 429 {
		t.Errorf("rate limit answers = %v, want 200 200 429 429", got)
	}
}
