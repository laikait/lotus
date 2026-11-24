package server

import (
	"bytes"
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

	"github.com/andybalholm/brotli"
	kgzip "github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/laikait/lotus/internal/compress"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

// ---- helpers ----

func withCompression(mutate ...func(*model.Compression)) func(*model.Site) {
	return func(s *model.Site) {
		c := model.DefaultCompression()
		for _, m := range mutate {
			m(&c)
		}
		s.Compression = &c
	}
}

var rawClient = &http.Client{
	Timeout:       5 * time.Second,
	Transport:     &http.Transport{DisableCompression: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// getRaw fetches without the client decoding anything.
func getRaw(t *testing.T, method, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	must(t, err)
	req.Host = "a.test"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := rawClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func unpack(t *testing.T, coding string, b []byte) string {
	t.Helper()
	var r io.Reader
	var err error
	switch coding {
	case "":
		return string(b)
	case "gzip":
		r, err = kgzip.NewReader(bytes.NewReader(b))
	case "br":
		r = brotli.NewReader(bytes.NewReader(b))
	case "zstd":
		var d *zstd.Decoder
		d, err = zstd.NewReader(bytes.NewReader(b))
		if err == nil {
			defer d.Close()
			r = d
		}
	default:
		t.Fatalf("coding %q", coding)
	}
	must(t, err)
	out, err := io.ReadAll(r)
	must(t, err)
	return string(out)
}

func pageOf(n int) string {
	return strings.Repeat("<li>an item of the list that repeats</li>\n", n/42+1)[:n]
}

// staticSite serves files from a document root, with compression per mutate.
func staticSite(t *testing.T, files map[string]string, mutate func(*model.Site)) (*harness, string, string) {
	t.Helper()
	cfg := routedConfig(t, mutate, nil)
	root := cfg.Sites[0].Root
	for name, body := range files {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		must(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
	}
	h := start(t, cfg)
	return h, "http://" + h.addr("http"), root
}

// ---- on-the-fly compression ----

func TestCompressionOfStaticFiles(t *testing.T) {
	big := pageOf(30000)
	_, base, _ := staticSite(t, map[string]string{
		"big.html": big, "small.html": "<p>tiny</p>", "pic.png": big, "data.json": `{"k":"` + strings.Repeat("v", 5000) + `"}`,
	}, withCompression())

	for _, coding := range []string{"br", "zstd", "gzip"} {
		resp, body := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": coding})
		if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != coding {
			t.Fatalf("%s: %d %q", coding, resp.StatusCode, resp.Header.Get("Content-Encoding"))
		}
		if unpack(t, coding, body) != big || len(body) > len(big)/5 {
			t.Fatalf("%s: round trip or ratio wrong (%d bytes)", coding, len(body))
		}
		if resp.Header.Get("Vary") != "Accept-Encoding" || resp.Header.Get("Content-Length") != fmt.Sprint(len(body)) {
			t.Errorf("%s: Vary=%q Content-Length=%q (%d)", coding, resp.Header.Get("Vary"), resp.Header.Get("Content-Length"), len(body))
		}
		if !strings.HasPrefix(resp.Header.Get("Etag"), "W/") {
			t.Errorf("%s: ETag %q", coding, resp.Header.Get("Etag"))
		}
	}
	// Without Accept-Encoding the file is sent as it is, but Vary says it could have been compressed.
	resp, body := getRaw(t, "GET", base+"/big.html", nil)
	if resp.Header.Get("Content-Encoding") != "" || string(body) != big || resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("plain client: %q %d bytes Vary=%q", resp.Header.Get("Content-Encoding"), len(body), resp.Header.Get("Vary"))
	}
	if resp.Header.Get("Content-Length") != fmt.Sprint(len(big)) {
		t.Errorf("Content-Length of the plain file: %q", resp.Header.Get("Content-Length"))
	}
	// Other types and small files are left alone, and need no Vary.
	for path, wantType := range map[string]string{"/pic.png": "image/png", "/small.html": "text/html"} {
		resp, _ := getRaw(t, "GET", base+path, map[string]string{"Accept-Encoding": "gzip, br"})
		if resp.Header.Get("Content-Encoding") != "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), wantType) {
			t.Errorf("%s: %q %q", path, resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))
		}
	}
	if resp, _ := getRaw(t, "GET", base+"/pic.png", map[string]string{"Accept-Encoding": "gzip"}); resp.Header.Get("Vary") != "" {
		t.Errorf("an image has Vary %q", resp.Header.Get("Vary"))
	}
	// JSON is in the default list.
	resp, body = getRaw(t, "GET", base+"/data.json", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(unpack(t, "gzip", body), `{"k":"vvv`) {
		t.Errorf("json: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressedStaticResponsesAndRanges(t *testing.T) {
	big := pageOf(30000)
	_, base, _ := staticSite(t, map[string]string{"big.html": big}, withCompression())

	// A Range request gets a part of the file as it is, not of the compressed bytes.
	resp, body := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=10-19"})
	if resp.StatusCode != 206 || string(body) != big[10:20] || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("range: %d %q enc=%q", resp.StatusCode, body, resp.Header.Get("Content-Encoding"))
	}
	// HEAD matches the GET's headers and has no body.
	g, _ := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "gzip"})
	h, hb := getRaw(t, "HEAD", base+"/big.html", map[string]string{"Accept-Encoding": "gzip"})
	if len(hb) != 0 || h.Header.Get("Content-Encoding") != "gzip" || h.Header.Get("Etag") != g.Header.Get("Etag") || h.Header.Get("Content-Length") != "" {
		t.Errorf("HEAD: %q etag %q (GET %q) len %d cl %q", h.Header.Get("Content-Encoding"), h.Header.Get("Etag"), g.Header.Get("Etag"), len(hb), h.Header.Get("Content-Length"))
	}
	// A conditional request still gets its 304: the weak ETag matches.
	resp, body = getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": g.Header.Get("Etag")})
	if resp.StatusCode != 304 || len(body) != 0 || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("conditional: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressionSettings(t *testing.T) {
	big := pageOf(30000)
	files := map[string]string{"big.html": big, "x.txt": big, "mid.html": pageOf(3000)}
	cases := []struct {
		name   string
		mutate func(*model.Compression)
		path   string
		accept string
		want   string
	}{
		{"algorithms restrict", func(c *model.Compression) { c.Algorithms = []string{"gzip"} }, "/big.html", "br, gzip", "gzip"},
		{"algorithm order", func(c *model.Compression) { c.Algorithms = []string{"zstd", "br"} }, "/big.html", "br, zstd, gzip", "zstd"},
		{"types", func(c *model.Compression) { c.Types = []string{"text/html"} }, "/x.txt", "gzip", ""},
		{"types allow html", func(c *model.Compression) { c.Types = []string{"text/html"} }, "/big.html", "gzip", "gzip"},
		{"min size", func(c *model.Compression) { c.MinSize = 5000 }, "/mid.html", "gzip", ""},
		{"min size allows more", func(c *model.Compression) { c.MinSize = 100 }, "/mid.html", "gzip", "gzip"},
		{"fast level", func(c *model.Compression) { c.Level = "fast" }, "/big.html", "br", "br"},
		{"best level", func(c *model.Compression) { c.Level = "best" }, "/big.html", "zstd", "zstd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, base, _ := staticSite(t, files, withCompression(c.mutate))
			resp, body := getRaw(t, "GET", base+c.path, map[string]string{"Accept-Encoding": c.accept})
			if resp.Header.Get("Content-Encoding") != c.want {
				t.Fatalf("Content-Encoding = %q, want %q", resp.Header.Get("Content-Encoding"), c.want)
			}
			if got := unpack(t, c.want, body); got != files[strings.TrimPrefix(c.path, "/")] {
				t.Fatal("the body is wrong")
			}
		})
	}
	// Without a compression block the site doesn't compress, and adds no Vary.
	_, base, _ := staticSite(t, files, nil)
	resp, body := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "gzip, br"})
	if resp.Header.Get("Content-Encoding") != "" || string(body) != big || resp.Header.Get("Vary") != "" {
		t.Errorf("no block: %q Vary=%q", resp.Header.Get("Content-Encoding"), resp.Header.Get("Vary"))
	}
}

func TestCompressionOfProxiedResponses(t *testing.T) {
	page := pageOf(20000)
	gz := func() []byte {
		var b bytes.Buffer
		w := kgzip.NewWriter(&b)
		io.WriteString(w, page)
		w.Close()
		return b.Bytes()
	}()
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/plain":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, page)
		case "/app/gzipped":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gz)
		case "/app/notransform":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cache-Control", "no-transform")
			io.WriteString(w, page)
		case "/app/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: 1\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(300 * time.Millisecond)
			io.WriteString(w, "data: 2\n\n")
		case "/app/chunked":
			w.Header().Set("Content-Type", "application/json")
			for i := 0; i < 20; i++ {
				io.WriteString(w, strings.Repeat(`{"a":"b"},`, 100))
				w.(http.Flusher).Flush() // the proxy flushes unsized responses like this
			}
		}
	}))
	defer be.Close()
	cfg := routedConfig(t, withCompression(), []model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"})})
	cfg.Upstreams = []model.Upstream{upstreamOf("app", be.URL)}
	h := start(t, cfg)
	base := "http://" + h.addr("http")

	resp, body := getRaw(t, "GET", base+"/app/plain", map[string]string{"Accept-Encoding": "br"})
	if resp.Header.Get("Content-Encoding") != "br" || unpack(t, "br", body) != page {
		t.Fatalf("plain: %q", resp.Header.Get("Content-Encoding"))
	}
	// A backend that compresses itself is passed through untouched.
	resp, body = getRaw(t, "GET", base+"/app/gzipped", map[string]string{"Accept-Encoding": "gzip, br"})
	if resp.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(body, gz) {
		t.Fatalf("gzipped: %q, %d bytes (want %d)", resp.Header.Get("Content-Encoding"), len(body), len(gz))
	}
	resp, body = getRaw(t, "GET", base+"/app/notransform", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "" || string(body) != page {
		t.Fatalf("no-transform: %q", resp.Header.Get("Content-Encoding"))
	}
	// An event stream is not compressed and arrives as it is produced.
	resp, _ = getRaw(t, "GET", base+"/app/stream", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("event stream compressed: %q", resp.Header.Get("Content-Encoding"))
	}
	// A chunked response of a compressible type is compressed as a stream.
	resp, body = getRaw(t, "GET", base+"/app/chunked", map[string]string{"Accept-Encoding": "gzip"})
	want := strings.Repeat(strings.Repeat(`{"a":"b"},`, 100), 20)
	if resp.Header.Get("Content-Encoding") != "gzip" || unpack(t, "gzip", body) != want {
		t.Fatalf("chunked: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressionOfPHPAndReturn(t *testing.T) {
	routes := append(phpRoutes(), model.Route{Kind: model.RouteExact, Path: "/text", Actions: ret(200, strings.Repeat("hello world, ", 300))})
	p := startPHP(t, func(r *fcgitest.Req) {
		r.Respond("Content-Type: text/html; charset=utf-8", pageOf(8000))
	}, routes, withCompression())
	resp, body := getRaw(t, "GET", p.base+"/app.php", map[string]string{"Accept-Encoding": "gzip"})
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "gzip" || unpack(t, "gzip", body) != pageOf(8000) {
		t.Fatalf("php: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	resp, body = getRaw(t, "GET", p.base+"/text", map[string]string{"Accept-Encoding": "zstd"})
	if resp.Header.Get("Content-Encoding") != "zstd" || !strings.HasPrefix(unpack(t, "zstd", body), "hello world, ") {
		t.Fatalf("return: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressionOfErrorPages(t *testing.T) {
	// An error page is a file like any other.
	cfg := routedConfig(t, func(s *model.Site) {
		withCompression()(s)
		s.ErrorPages = map[int]string{404: "/404.html"}
	}, nil)
	must(t, os.WriteFile(filepath.Join(cfg.Sites[0].Root, "404.html"), []byte(pageOf(4000)), 0o644))
	h := start(t, cfg)
	resp, body := getRaw(t, "GET", "http://"+h.addr("http")+"/nope", map[string]string{"Accept-Encoding": "gzip"})
	if resp.StatusCode != 404 || resp.Header.Get("Content-Encoding") != "gzip" || unpack(t, "gzip", body) != pageOf(4000) {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressionAfterTheCache(t *testing.T) {
	// A cached page is stored once and compressed for each client that accepts it.
	n := 0
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, pageOf(9000))
	}))
	defer be.Close()
	rc := defaultRouteCache()
	r := cacheRoute(rc)
	cfg := routedConfig(t, withCompression(), []model.Route{r})
	cfg.Upstreams = []model.Upstream{upstreamOf("app", be.URL)}
	cfg.Caches = []model.CacheZone{testZone()}
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	for i, want := range []string{"MISS", "HIT", "HIT"} {
		enc := []string{"gzip", "br", ""}[i]
		resp, body := getRaw(t, "GET", base+"/app/c", map[string]string{"Accept-Encoding": enc})
		if resp.Header.Get("X-Cache") != want || resp.Header.Get("Content-Encoding") != enc || unpack(t, enc, body) != pageOf(9000) {
			t.Fatalf("round %d: %s enc=%q", i, resp.Header.Get("X-Cache"), resp.Header.Get("Content-Encoding"))
		}
	}
	if n != 1 {
		t.Fatalf("backend saw %d requests", n)
	}
	// A range on a hit is of the stored bytes.
	resp, body := getRaw(t, "GET", base+"/app/c", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-9"})
	if resp.StatusCode != 206 || string(body) != pageOf(9000)[:10] || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("range on a hit: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
}

func TestACutBackendBodyIsNotMadeToLookComplete(t *testing.T) {
	// The backend promises a long body and drops the connection half way.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				c.Read(buf)
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", 6000, pageOf(6000))
				time.Sleep(100 * time.Millisecond) // then the connection just closes: no last chunk
			}()
		}
	}()
	cfg := routedConfig(t, withCompression(), []model.Route{proxyRoute("/app/", model.Actions{Proxy: "app"})})
	cfg.Upstreams = []model.Upstream{upstreamOf("app", "http://"+ln.Addr().String())}
	h := start(t, cfg)
	req, _ := http.NewRequest("GET", "http://"+h.addr("http")+"/app/cut", nil)
	req.Host = "a.test"
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, rerr := io.ReadAll(resp.Body)
	zr, zerr := kgzip.NewReader(bytes.NewReader(raw))
	if zerr == nil {
		_, zerr = io.ReadAll(zr)
	}
	if rerr == nil {
		t.Fatal("the connection was not cut: the client can't tell the response is incomplete")
	}
	if zerr == nil {
		t.Fatal("a response cut short at the backend reached the client as a complete gzip stream")
	}
}

func TestCompressionMetrics(t *testing.T) {
	big := pageOf(30000)
	cfg := withMetrics(routedConfig(t, withCompression(), nil))
	must(t, os.WriteFile(filepath.Join(cfg.Sites[0].Root, "big.html"), []byte(big), 0o644))
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	var out int
	for _, c := range []string{"gzip", "gzip", "br"} {
		_, body := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": c})
		out += len(body)
	}
	getRaw(t, "GET", base+"/big.html", nil) // not compressed: not counted

	fams, _ := scrape(t, h, nil)
	if v, ok := fams.value(t, "lotus_http_compressed_responses_total", map[string]string{"site": "a.test", "coding": "gzip"}); !ok || v != 2 {
		t.Errorf("gzip responses = %v", v)
	}
	if v, ok := fams.value(t, "lotus_http_compressed_responses_total", map[string]string{"site": "a.test", "coding": "br"}); !ok || v != 1 {
		t.Errorf("br responses = %v", v)
	}
	if v, _ := fams.value(t, "lotus_http_compression_input_bytes_total", map[string]string{"site": "a.test"}); v != float64(3*len(big)) {
		t.Errorf("input bytes = %v, want %d", v, 3*len(big))
	}
	if v, _ := fams.value(t, "lotus_http_compression_output_bytes_total", map[string]string{"site": "a.test"}); v != float64(out) {
		t.Errorf("output bytes = %v, want %d", v, out)
	}
	if _, ok := fams.value(t, "lotus_compression_active", nil); !ok {
		t.Error("lotus_compression_active missing")
	}
	if _, ok := fams.value(t, "lotus_compression_skipped_busy_total", nil); !ok {
		t.Error("lotus_compression_skipped_busy_total missing")
	}
}

func TestCompressionSettingsChangeOnReload(t *testing.T) {
	big := pageOf(10000)
	cfg := routedConfig(t, withCompression(func(c *model.Compression) { c.Algorithms = []string{"gzip"} }), nil)
	must(t, os.WriteFile(filepath.Join(cfg.Sites[0].Root, "big.html"), []byte(big), 0o644))
	h := start(t, cfg)
	base := "http://" + h.addr("http")
	if resp, _ := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "br, gzip"}); resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("before")
	}
	next := routedConfig(t, withCompression(func(c *model.Compression) { c.Algorithms = []string{"br", "gzip"} }), nil)
	next.Sites[0].Root, next.Listeners = cfg.Sites[0].Root, cfg.Listeners
	must(t, h.srv.Reload(next))
	if resp, _ := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "br, gzip"}); resp.Header.Get("Content-Encoding") != "br" {
		t.Fatal("after the reload")
	}
	off := routedConfig(t, nil, nil)
	off.Sites[0].Root, off.Listeners = cfg.Sites[0].Root, cfg.Listeners
	must(t, h.srv.Reload(off))
	if resp, _ := getRaw(t, "GET", base+"/big.html", map[string]string{"Accept-Encoding": "br, gzip"}); resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("after turning it off")
	}
}

func TestDefaultTypesAgree(t *testing.T) {
	a, b := model.DefaultCompressionTypes, compress.DefaultTypes
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("the configuration's default types and the compressor's differ:\n%v\n%v", a, b)
	}
}

// ---- precompressed files ----

func writeCopy(t *testing.T, root, name string, body []byte, mtime time.Time) {
	t.Helper()
	p := filepath.Join(root, name)
	must(t, os.WriteFile(p, body, 0o644))
	must(t, os.Chtimes(p, mtime, mtime))
}

func encodeAs(t *testing.T, coding, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	var w interface {
		io.Writer
		Close() error
	}
	switch coding {
	case "gzip":
		w = kgzip.NewWriter(&b)
	case "br":
		w = brotli.NewWriter(&b)
	case "zstd":
		z, err := zstd.NewWriter(&b)
		must(t, err)
		w = z
	}
	io.WriteString(w, s)
	must(t, w.Close())
	return b.Bytes()
}

func TestPrecompressedFiles(t *testing.T) {
	js := strings.Repeat("function f(){return 1}\n", 400)
	_, base, root := staticSite(t, map[string]string{"app.js": js, "plain.js": js}, withCompression(func(c *model.Compression) { c.Precompressed = true }))
	old := time.Now().Add(-time.Hour)
	brBody, zBody, gzBody := encodeAs(t, "br", js), encodeAs(t, "zstd", js), encodeAs(t, "gzip", js)
	for _, f := range []string{"app.js", "plain.js"} {
		must(t, os.Chtimes(filepath.Join(root, f), old.Add(-time.Hour), old.Add(-time.Hour)))
	}
	writeCopy(t, root, "app.js.br", brBody, old)
	writeCopy(t, root, "app.js.zst", zBody, old)
	writeCopy(t, root, "app.js.gz", gzBody, old)
	writeCopy(t, root, "orphan.js.br", brBody, old) // no original

	identity, _ := getRaw(t, "GET", base+"/app.js", nil)
	for accept, want := range map[string][]byte{"br, gzip": brBody, "zstd, gzip": zBody, "gzip": gzBody, "gzip, br;q=0.1": gzBody} {
		resp, body := getRaw(t, "GET", base+"/app.js", map[string]string{"Accept-Encoding": accept})
		coding := resp.Header.Get("Content-Encoding")
		if !bytes.Equal(body, want) || coding == "" {
			t.Errorf("Accept-Encoding %q: %q, %d bytes (want %d)", accept, coding, len(body), len(want))
			continue
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") && !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/javascript") {
			t.Errorf("%q: Content-Type %q is the copy's, not the file's", accept, resp.Header.Get("Content-Type"))
		}
		if resp.Header.Get("Vary") != "Accept-Encoding" || resp.Header.Get("Etag") == identity.Header.Get("Etag") || resp.Header.Get("Content-Length") != fmt.Sprint(len(want)) {
			t.Errorf("%q: Vary=%q Etag=%q (identity %q) CL=%q", accept, resp.Header.Get("Vary"), resp.Header.Get("Etag"), identity.Header.Get("Etag"), resp.Header.Get("Content-Length"))
		}
		if unpack(t, coding, body) != js {
			t.Errorf("%q: the copy does not decode to the file", accept)
		}
	}
	// A client that accepts nothing gets the file, and Vary says so.
	resp, body := getRaw(t, "GET", base+"/app.js", nil)
	if string(body) != js || resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("plain: %q Vary=%q", resp.Header.Get("Content-Encoding"), resp.Header.Get("Vary"))
	}
	// A file without copies is compressed on the fly (the block is on).
	resp, body = getRaw(t, "GET", base+"/plain.js", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "gzip" || unpack(t, "gzip", body) != js {
		t.Errorf("no copy: %q", resp.Header.Get("Content-Encoding"))
	}
	// A copy alone is not a file.
	if resp, _ := getRaw(t, "GET", base+"/orphan.js", map[string]string{"Accept-Encoding": "br"}); resp.StatusCode != 404 {
		t.Errorf("orphan copy: %d", resp.StatusCode)
	}
	// Ranges are of the copy, which is what the client was told it is getting.
	resp, body = getRaw(t, "GET", base+"/app.js", map[string]string{"Accept-Encoding": "br", "Range": "bytes=0-9"})
	if resp.StatusCode != 206 || !bytes.Equal(body, brBody[:10]) || resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("range: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	// HEAD and conditional requests agree with GET.
	g, _ := getRaw(t, "GET", base+"/app.js", map[string]string{"Accept-Encoding": "br"})
	if hr, hb := getRaw(t, "HEAD", base+"/app.js", map[string]string{"Accept-Encoding": "br"}); len(hb) != 0 || hr.Header.Get("Content-Encoding") != "br" || hr.Header.Get("Etag") != g.Header.Get("Etag") {
		t.Errorf("HEAD: %q", hr.Header.Get("Content-Encoding"))
	}
	if r, _ := getRaw(t, "GET", base+"/app.js", map[string]string{"Accept-Encoding": "br", "If-None-Match": g.Header.Get("Etag")}); r.StatusCode != 304 {
		t.Errorf("conditional: %d", r.StatusCode)
	}
}

func TestStalePrecompressedCopiesAreIgnored(t *testing.T) {
	js := strings.Repeat("var a = 1;\n", 300)
	_, base, root := staticSite(t, map[string]string{"s.js": js}, withCompression(func(c *model.Compression) { c.Precompressed = true; c.MinSize = 1 << 20 }))
	now := time.Now()
	must(t, os.Chtimes(filepath.Join(root, "s.js"), now, now)) // the file changed after the copy was made
	writeCopy(t, root, "s.js.gz", encodeAs(t, "gzip", "OLD CONTENT"), now.Add(-time.Hour))
	resp, body := getRaw(t, "GET", base+"/s.js", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "" || string(body) != js {
		t.Fatalf("a copy older than the file was served: %q", resp.Header.Get("Content-Encoding"))
	}
	// An equal time counts as current (copies are usually made in the same second).
	writeCopy(t, root, "s.js.gz", encodeAs(t, "gzip", js), now)
	resp, body = getRaw(t, "GET", base+"/s.js", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "gzip" || unpack(t, "gzip", body) != js {
		t.Fatalf("a copy as new as the file was ignored: %q", resp.Header.Get("Content-Encoding"))
	}
	// An empty copy is never served.
	writeCopy(t, root, "s.js.gz", nil, now.Add(time.Hour))
	if resp, body := getRaw(t, "GET", base+"/s.js", map[string]string{"Accept-Encoding": "gzip"}); resp.Header.Get("Content-Encoding") != "" || string(body) != js {
		t.Fatal("an empty copy was served")
	}
}

func TestPrecompressedCopiesFollowTheFileRules(t *testing.T) {
	js := strings.Repeat("var a = 1;\n", 300)
	_, base, root := staticSite(t, map[string]string{"link.js": js, "dir.js": js}, withCompression(func(c *model.Compression) { c.Precompressed = true; c.MinSize = 1 << 20 }))
	now := time.Now().Add(time.Hour)
	// A copy that is a directory is not a copy.
	must(t, os.MkdirAll(filepath.Join(root, "dir.js.gz"), 0o755))
	if resp, body := getRaw(t, "GET", base+"/dir.js", map[string]string{"Accept-Encoding": "gzip"}); resp.StatusCode != 200 || string(body) != js || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("directory as a copy: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	// A copy that is a symlink out of the document root is refused like any such file.
	outside := filepath.Join(t.TempDir(), "secret.gz")
	must(t, os.WriteFile(outside, encodeAs(t, "gzip", "SECRET"), 0o644))
	if err := os.Symlink(outside, filepath.Join(root, "link.js.gz")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	must(t, os.Chtimes(outside, now, now))
	resp, body := getRaw(t, "GET", base+"/link.js", map[string]string{"Accept-Encoding": "gzip"})
	if strings.Contains(string(body), "SECRET") || (resp.Header.Get("Content-Encoding") != "" && unpack(t, "gzip", body) == "SECRET") {
		t.Fatal("a precompressed copy outside the document root was served")
	}
}

func TestPrecompressedWithoutTheOptionIsNotUsed(t *testing.T) {
	js := strings.Repeat("var a = 1;\n", 300)
	_, base, root := staticSite(t, map[string]string{"n.js": js}, withCompression(func(c *model.Compression) { c.MinSize = 1 << 20 }))
	writeCopy(t, root, "n.js.gz", encodeAs(t, "gzip", "COPY"), time.Now().Add(time.Hour))
	resp, body := getRaw(t, "GET", base+"/n.js", map[string]string{"Accept-Encoding": "gzip"})
	if string(body) != js || resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("a copy was used although precompressed is off")
	}
}

func TestPrecompressedFilesAreNotCompressedAgain(t *testing.T) {
	js := strings.Repeat("function g(){}\n", 500)
	_, base, root := staticSite(t, map[string]string{"app.js": js}, withCompression(func(c *model.Compression) { c.Precompressed = true }))
	cp := encodeAs(t, "br", js)
	writeCopy(t, root, "app.js.br", cp, time.Now().Add(time.Hour))
	resp, body := getRaw(t, "GET", base+"/app.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if !bytes.Equal(body, cp) || resp.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("%q, %d bytes (want the copy, %d)", resp.Header.Get("Content-Encoding"), len(body), len(cp))
	}
}

func TestCompressConfigMapsTheSiteSettings(t *testing.T) {
	s := &Server{compLimiter: compress.NewLimiter(0)}
	st := &siteStats{}
	if s.compressConfig(&model.Site{}, st) != nil {
		t.Fatal("a site without compression got a configuration")
	}
	for level, want := range map[string]compress.Level{"default": compress.LevelDefault, "fast": compress.LevelFast, "best": compress.LevelBest} {
		c := model.DefaultCompression()
		c.Level, c.MinSize, c.Algorithms, c.Types = level, 77, []string{"gzip"}, []string{"text/css"}
		cfg := s.compressConfig(&model.Site{Compression: &c}, st)
		if cfg == nil || cfg.Level != want || cfg.MinSize != 77 || len(cfg.Algorithms) != 1 || cfg.Types[0] != "text/css" || cfg.Limiter != s.compLimiter || cfg.Observe == nil {
			t.Fatalf("level %s: %+v", level, cfg)
		}
	}
	c := model.DefaultCompression()
	cfg := s.compressConfig(&model.Site{Compression: &c}, st)
	cfg.Observe("br", 1000, 100)
	cfg.Observe("zstd", 500, 60)
	cfg.Observe("gzip", 10, 5)
	cfg.Observe("gzip", 10, 5)
	if st.compressed[0].Load() != 1 || st.compressed[1].Load() != 1 || st.compressed[2].Load() != 2 || st.compIn.Load() != 1520 || st.compOut.Load() != 170 {
		t.Errorf("%d %d %d in=%d out=%d", st.compressed[0].Load(), st.compressed[1].Load(), st.compressed[2].Load(), st.compIn.Load(), st.compOut.Load())
	}
}
