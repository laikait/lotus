package compress

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	kgzip "github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

func TestNegotiate(t *testing.T) {
	all := []string{"br", "zstd", "gzip"}
	cases := []struct {
		name, header string
		offered      []string
		want         string // comma separated
	}{
		{"empty header", "", all, ""},
		{"gzip only", "gzip", all, "gzip"},
		{"server order wins on equal weight", "gzip, br, zstd", all, "br,zstd,gzip"},
		{"client weights win", "gzip;q=1.0, br;q=0.5", all, "gzip,br"},
		{"q=0 refuses", "br;q=0, gzip", all, "gzip"},
		{"unnamed is not accepted", "deflate", all, ""},
		{"wildcard", "*", all, "br,zstd,gzip"},
		{"wildcard with a refusal", "*, br;q=0", all, "zstd,gzip"},
		{"wildcard weight", "gzip, *;q=0.1", all, "gzip,br,zstd"},
		{"wildcard zero", "gzip, *;q=0", all, "gzip"},
		{"x-gzip is gzip", "x-gzip", all, "gzip"},
		{"case and spaces", " GZIP ; Q=0.8 ,  Br ", all, "br,gzip"},
		{"only offered ones", "br, zstd", []string{"gzip"}, ""},
		{"nothing offered", "gzip", nil, ""},
		{"unreadable weight is no offer", "gzip;q=abc, br", all, "br"},
		{"negative weight is no offer", "gzip;q=-1, br", all, "br"},
		{"identity is not a coding to produce", "identity", all, ""},
		{"empty items", ",,gzip,,", all, "gzip"},
	}
	for _, c := range cases {
		if got := strings.Join(Negotiate(c.header, c.offered), ","); got != c.want {
			t.Errorf("%s: %q -> %q, want %q", c.name, c.header, got, c.want)
		}
	}
}

// ---- helpers ----

func decode(t *testing.T, coding string, body []byte) []byte {
	t.Helper()
	var r io.Reader
	var err error
	switch coding {
	case "gzip":
		r, err = kgzip.NewReader(bytes.NewReader(body))
	case "br":
		r = brotli.NewReader(bytes.NewReader(body))
	case "zstd":
		var d *zstd.Decoder
		if d, err = zstd.NewReader(bytes.NewReader(body)); err == nil {
			defer d.Close()
			r = d
		}
	default:
		t.Fatalf("unknown coding %q", coding)
	}
	if err != nil {
		t.Fatalf("%s: %v", coding, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v (%d bytes)", coding, err, len(body))
	}
	return out
}

func testConfig() *Config {
	return &Config{Algorithms: []string{"br", "zstd", "gzip"}, Types: DefaultTypes, MinSize: 1024}
}

func html(n int) string {
	return strings.Repeat("<p>hello compression, this paragraph repeats</p>\n", n/49+1)[:n]
}

// run sends a request through a Writer and returns what the client would see.
func run(t *testing.T, cfg *Config, method, accept string, h func(w http.ResponseWriter)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/x", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	cw := New(rec, req, cfg)
	h(cw)
	if err := cw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return rec
}

func sized(ct string, body string, extra ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		for i := 0; i+1 < len(extra); i += 2 {
			w.Header().Add(extra[i], extra[i+1])
		}
		io.WriteString(w, body)
	}
}

func unsized(ct string, body string, chunk int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", ct)
		for i := 0; i < len(body); i += chunk {
			end := i + chunk
			if end > len(body) {
				end = len(body)
			}
			io.WriteString(w, body[i:end])
		}
	}
}

// ---- tests ----

func TestCompressesAndRoundTrips(t *testing.T) {
	body := html(20000)
	for _, coding := range []string{"gzip", "br", "zstd"} {
		t.Run(coding, func(t *testing.T) {
			rec := run(t, testConfig(), "GET", coding, sized("text/html; charset=utf-8", body, "ETag", `"abc"`, "Accept-Ranges", "bytes"))
			h := rec.Header()
			if h.Get("Content-Encoding") != coding {
				t.Fatalf("Content-Encoding = %q", h.Get("Content-Encoding"))
			}
			if h.Get("Content-Length") != "" || h.Get("Accept-Ranges") != "" {
				t.Errorf("Content-Length %q / Accept-Ranges %q survived", h.Get("Content-Length"), h.Get("Accept-Ranges"))
			}
			if h.Get("ETag") != `W/"abc"` {
				t.Errorf("ETag = %q, want a weak one", h.Get("ETag"))
			}
			if got := h.Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
				t.Errorf("Vary = %v", got)
			}
			if rec.Body.Len() >= len(body)/4 {
				t.Errorf("%d bytes for %d: not compressed", rec.Body.Len(), len(body))
			}
			if got := decode(t, coding, rec.Body.Bytes()); string(got) != body {
				t.Fatalf("round trip lost data (%d vs %d bytes)", len(got), len(body))
			}
		})
	}
}

func TestChoosesTheBestAcceptedCoding(t *testing.T) {
	body := html(5000)
	for accept, want := range map[string]string{
		"gzip, br":             "br",
		"gzip, zstd":           "zstd",
		"gzip":                 "gzip",
		"br;q=0.1, gzip;q=0.9": "gzip",
		"*":                    "br",
	} {
		rec := run(t, testConfig(), "GET", accept, sized("text/html", body))
		if got := rec.Header().Get("Content-Encoding"); got != want {
			t.Errorf("Accept-Encoding %q -> %q, want %q", accept, got, want)
		}
	}
	cfg := testConfig()
	cfg.Algorithms = []string{"gzip"} // only what is configured is offered
	if got := run(t, cfg, "GET", "br, gzip", sized("text/html", body)).Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("offered gzip only: %q", got)
	}
}

func TestUnknownLengthIsBufferedUntilItIsBigEnough(t *testing.T) {
	body := html(20000)
	rec := run(t, testConfig(), "GET", "gzip", unsized("text/html", body, 100))
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("%v", rec.Header())
	}
	if got := decode(t, "gzip", rec.Body.Bytes()); string(got) != body {
		t.Fatal("round trip lost data")
	}
}

func TestSmallBodiesAreLeftAlone(t *testing.T) {
	small := html(1023) // one byte below min_size
	for name, h := range map[string]func(http.ResponseWriter){
		"known length":   sized("text/html", small),
		"unknown length": unsized("text/html", small, 100),
	} {
		rec := run(t, testConfig(), "GET", "gzip", h)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != small {
			t.Errorf("%s: a small body was compressed (%q, %d bytes)", name, rec.Header().Get("Content-Encoding"), rec.Body.Len())
		}
		if rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("%s: Vary = %q; the answer still depends on the client", name, rec.Header().Get("Vary"))
		}
	}
	// Exactly min_size is compressed.
	if rec := run(t, testConfig(), "GET", "gzip", sized("text/html", html(1024))); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Error("a body of exactly min_size was not compressed")
	}
	if rec := run(t, testConfig(), "GET", "gzip", unsized("text/html", html(1024), 1024)); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Error("an unsized body of exactly min_size was not compressed")
	}
}

func TestWhatIsNeverCompressed(t *testing.T) {
	body := html(5000)
	status := func(code int) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(code)
			if code != 204 && code != 304 {
				io.WriteString(w, body)
			}
		}
	}
	cases := []struct {
		name     string
		method   string
		accept   string
		h        func(http.ResponseWriter)
		wantVary bool // the type is compressible, so Vary is expected
	}{
		{"already encoded", "GET", "gzip", sized("text/html", body, "Content-Encoding", "br"), false},
		{"partial content", "GET", "gzip", sized("text/html", body, "Content-Range", "bytes 0-9/100"), false},
		{"no-transform", "GET", "gzip", sized("text/html", body, "Cache-Control", "public, No-Transform, max-age=5"), false},
		{"no-transform on another line", "GET", "gzip", sized("text/html", body, "Cache-Control", "max-age=5", "Cache-Control", "no-transform"), false},
		{"an image", "GET", "gzip", sized("image/png", body), false},
		{"a video", "GET", "gzip", sized("video/mp4", body), false},
		{"no content type", "GET", "gzip", func(w http.ResponseWriter) { io.WriteString(w, body) }, false},
		{"client refuses", "GET", "gzip;q=0", sized("text/html", body), true},
		{"client sends nothing", "GET", "", sized("text/html", body), true},
		{"client wants something we lack", "GET", "deflate", sized("text/html", body), true},
		{"206", "GET", "gzip", status(206), false},
		{"304", "GET", "gzip", status(304), false},
		{"204", "GET", "gzip", status(204), false},
		{"HEAD without a length", "HEAD", "gzip", unsized("text/html", body, 500), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := run(t, testConfig(), c.method, c.accept, c.h)
			if enc := rec.Header().Get("Content-Encoding"); enc != "" && enc != "br" {
				t.Fatalf("compressed with %q", enc)
			}
			if c.name == "already encoded" && rec.Header().Get("Content-Encoding") != "br" {
				t.Fatal("the backend's own Content-Encoding was changed")
			}
			if c.method != "HEAD" && c.name != "206" && c.name != "304" && c.name != "204" && rec.Body.String() != body {
				t.Fatalf("the body was altered (%d bytes)", rec.Body.Len())
			}
			if got := strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding"); got != c.wantVary {
				t.Fatalf("Vary = %q, want Accept-Encoding present: %v", rec.Header().Get("Vary"), c.wantVary)
			}
			if c.name != "an image" && c.name != "client refuses" && strings.Contains(rec.Header().Get("ETag"), "W/") {
				t.Fatal("ETag changed on an uncompressed response")
			}
		})
	}
}

func TestHeadGetsTheHeadersOfTheGet(t *testing.T) {
	body := html(5000)
	rec := run(t, testConfig(), "HEAD", "gzip", sized("text/html", body, "ETag", `"x"`))
	h := rec.Header()
	if h.Get("Content-Encoding") != "gzip" || h.Get("Content-Length") != "" || h.Get("ETag") != `W/"x"` {
		t.Fatalf("%v", h)
	}
}

func TestVaryIsAddedOnce(t *testing.T) {
	body := html(3000)
	for name, c := range map[string]struct {
		in   []string
		want string
	}{
		"none":      {nil, "Accept-Encoding"},
		"other":     {[]string{"Origin"}, "Origin, Accept-Encoding"},
		"already":   {[]string{"accept-encoding, Origin"}, "accept-encoding, Origin"},
		"star":      {[]string{"*"}, "*"},
		"two lines": {[]string{"Origin", "Accept-Encoding"}, "Origin, Accept-Encoding"},
	} {
		var extra []string
		for _, v := range c.in {
			extra = append(extra, "Vary", v)
		}
		rec := run(t, testConfig(), "GET", "gzip", sized("text/html", body, extra...))
		if got := strings.Join(rec.Header().Values("Vary"), ", "); got != c.want {
			t.Errorf("%s: Vary = %q, want %q", name, got, c.want)
		}
	}
}

func TestWildcardTypes(t *testing.T) {
	cfg := &Config{Algorithms: []string{"gzip"}, Types: []string{"text/*", "application/json"}, MinSize: 10}
	for ct, want := range map[string]bool{
		"text/html": true, "TEXT/CSS; charset=utf-8": true, "text/x-anything": true,
		"application/json": true, "application/json; charset=utf-8": true,
		"text/event-stream": false, "text/event-stream; charset=utf-8": false,
		"application/jsonx": false, "application/xml": false, "textual/html": false, "image/png": false, "": false,
	} {
		if got := cfg.Compressible(mediaType(ct)); got != want {
			t.Errorf("Compressible(%q) = %v, want %v", ct, got, want)
		}
	}
}

func TestConfiguredTypesAreNormalized(t *testing.T) {
	cfg := &Config{Algorithms: []string{"gzip"}, Types: []string{" Text/HTML ", "APPLICATION/*"}, MinSize: 1}
	if !cfg.Compressible("text/html") || !cfg.Compressible("application/json") || cfg.Compressible("image/png") {
		t.Error("types written with capitals or spaces don't match")
	}
}

func TestEmptyResponses(t *testing.T) {
	rec := run(t, testConfig(), "GET", "gzip", func(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/html") })
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("nothing written: %d bytes, %q", rec.Body.Len(), rec.Header().Get("Content-Encoding"))
	}
	rec = run(t, testConfig(), "GET", "gzip", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
	})
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" || rec.Code != 200 {
		t.Errorf("empty 200: %d bytes, %q, %d", rec.Body.Len(), rec.Header().Get("Content-Encoding"), rec.Code)
	}
}

func TestImplicitStatusAndSecondWriteHeader(t *testing.T) {
	rec := run(t, testConfig(), "GET", "gzip", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(404)
		w.WriteHeader(200) // ignored
		io.WriteString(w, html(3000))
	})
	if rec.Code != 404 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("%d %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	// Error responses are compressed like any other.
	if got := decode(t, "gzip", rec.Body.Bytes()); string(got) != html(3000) {
		t.Error("round trip")
	}
}

func TestObserveReportsSizes(t *testing.T) {
	var coding string
	var in, out int64
	cfg := testConfig()
	cfg.Observe = func(c string, i, o int64) { coding, in, out = c, i, o }
	body := html(10000)
	rec := run(t, cfg, "GET", "zstd", sized("text/html", body))
	if coding != "zstd" || in != int64(len(body)) || out != int64(rec.Body.Len()) || out >= in {
		t.Errorf("observed %s %d -> %d; body was %d -> %d", coding, in, out, len(body), rec.Body.Len())
	}
	coding = ""
	run(t, cfg, "GET", "zstd", sized("image/png", body))
	run(t, cfg, "GET", "zstd", sized("text/html", "tiny"))
	if coding != "" {
		t.Error("Observe was called for a response that was not compressed")
	}
}

func TestLimiterSkipsCompressionWhenBusy(t *testing.T) {
	cfg := testConfig()
	cfg.Limiter = NewLimiter(1)
	body := html(5000)
	req := func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		return r
	}
	first := httptest.NewRecorder()
	w1 := New(first, req(), cfg)
	w1.Header().Set("Content-Type", "text/html")
	w1.WriteHeader(200) // holds the only slot, streaming
	if cfg.Limiter.Active() != 1 {
		t.Fatalf("active = %d", cfg.Limiter.Active())
	}
	second := run(t, cfg, "GET", "gzip", sized("text/html", body))
	if second.Header().Get("Content-Encoding") != "" || second.Body.String() != body {
		t.Fatal("a response past the limit was compressed")
	}
	if cfg.Limiter.Busy() != 1 {
		t.Errorf("busy = %d", cfg.Limiter.Busy())
	}
	w1.Close()
	if cfg.Limiter.Active() != 0 {
		t.Fatalf("the slot was not given back: %d", cfg.Limiter.Active())
	}
	if rec := run(t, cfg, "GET", "gzip", sized("text/html", body)); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Error("compression did not resume after the slot was freed")
	}
	// Slots are also given back when the response was passed on, aborted or small.
	run(t, cfg, "GET", "gzip", unsized("text/html", "small", 2))
	ab := New(httptest.NewRecorder(), req(), cfg)
	ab.Header().Set("Content-Type", "text/html")
	ab.Write([]byte(html(5000)))
	ab.Abort()
	if cfg.Limiter.Active() != 0 {
		t.Fatalf("slots leaked: %d", cfg.Limiter.Active())
	}
}

func TestAbortLeavesAnIncompleteStream(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	cw := New(rec, req, testConfig())
	cw.Header().Set("Content-Type", "text/html")
	io.WriteString(cw, html(5000))
	cw.Flush()
	cw.Abort()
	r, err := kgzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if err == nil {
		t.Fatal("an aborted response looks complete to the client")
	}
	// A normal close does.
	rec2 := run(t, testConfig(), "GET", "gzip", unsized("text/html", html(5000), 700))
	if got := decode(t, "gzip", rec2.Body.Bytes()); string(got) != html(5000) {
		t.Fatal("a closed response is not complete")
	}
}

// rfWriter is a ResponseWriter with ReadFrom, like the one net/http gives.
type rfWriter struct {
	*httptest.ResponseRecorder
	readFroms int
}

func (w *rfWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFroms++
	return io.Copy(w.ResponseRecorder, r)
}

func TestReadFromKeepsSendfileForUncompressedResponses(t *testing.T) {
	body := html(50000)
	run := func(ct, accept string) (*rfWriter, *Writer) {
		req := httptest.NewRequest("GET", "/f", nil)
		req.Header.Set("Accept-Encoding", accept)
		rw := &rfWriter{ResponseRecorder: httptest.NewRecorder()}
		cw := New(rw, req, testConfig())
		cw.Header().Set("Content-Type", ct)
		cw.Header().Set("Content-Length", fmt.Sprint(len(body)))
		cw.WriteHeader(200)
		if _, err := io.Copy(cw, io.LimitReader(strings.NewReader(body), int64(len(body)))); err != nil {
			t.Fatal(err)
		}
		if err := cw.Close(); err != nil {
			t.Fatal(err)
		}
		return rw, cw
	}
	rw, _ := run("image/png", "gzip")
	if rw.readFroms != 1 || rw.Body.String() != body {
		t.Fatalf("an image: ReadFrom called %d times, body ok: %v", rw.readFroms, rw.Body.String() == body)
	}
	rw, _ = run("text/html", "")
	if rw.readFroms != 1 || rw.Body.String() != body {
		t.Fatalf("a client without compression: ReadFrom called %d times", rw.readFroms)
	}
	rw, _ = run("text/html", "gzip")
	if rw.readFroms != 0 {
		t.Fatalf("a compressed response used the underlying ReadFrom %d times", rw.readFroms)
	}
	if got := decode(t, "gzip", rw.Body.Bytes()); string(got) != body {
		t.Fatal("compressed ReadFrom lost data")
	}
}

func TestLargeBodiesAndReuseOfEncoders(t *testing.T) {
	var big strings.Builder
	for i := 0; big.Len() < 3<<20; i++ {
		fmt.Fprintf(&big, "line %d: %x\n", i, i*2654435761)
	}
	for _, coding := range []string{"gzip", "br", "zstd"} {
		// Encoders are pooled: later responses must not carry anything of earlier ones.
		for round := 0; round < 4; round++ {
			body := big.String()[:len(big.String())/(round+1)]
			rec := run(t, testConfig(), "GET", coding, unsized("text/plain", body, 64<<10))
			if got := decode(t, coding, rec.Body.Bytes()); string(got) != body {
				t.Fatalf("%s round %d: %d vs %d bytes", coding, round, len(got), len(body))
			}
		}
	}
}

func TestConcurrentResponses(t *testing.T) {
	var wg sync.WaitGroup
	cfg := testConfig()
	cfg.Limiter = NewLimiter(64)
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			coding := []string{"gzip", "br", "zstd"}[i%3]
			body := fmt.Sprintf("response %d ", i) + html(2000+i*37)
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", coding)
			rec := httptest.NewRecorder()
			cw := New(rec, req, cfg)
			cw.Header().Set("Content-Type", "text/html")
			for j := 0; j < len(body); j += 300 {
				end := j + 300
				if end > len(body) {
					end = len(body)
				}
				io.WriteString(cw, body[j:end])
			}
			cw.Close()
			if got := decode(t, coding, rec.Body.Bytes()); string(got) != body {
				t.Errorf("response %d (%s) was corrupted", i, coding)
			}
		}(i)
	}
	wg.Wait()
	if cfg.Limiter.Active() != 0 {
		t.Fatalf("slots leaked: %d", cfg.Limiter.Active())
	}
}

// A real server: flushes stream, 1xx passes, and the compressed stream is
// readable while the handler is still running.
func TestStreamingThroughARealServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := New(w, r, testConfig())
		defer cw.Close()
		cw.Header().Set("Content-Type", "text/plain")
		cw.Header().Set("Link", "</a.css>; rel=preload")
		cw.WriteHeader(http.StatusEarlyHints)
		cw.Header().Del("Link")
		io.WriteString(cw, "first chunk\n") // far below min_size
		cw.Flush()                          // a flush turns it into a stream
		<-release
		io.WriteString(cw, "second chunk\n")
	}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("a flushed stream was not compressed: %v", resp.Header)
	}
	zr, err := kgzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("first chunk\n"))
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(zr, got); done <- err }()
	select {
	case err := <-done:
		if err != nil || string(got) != "first chunk\n" {
			t.Fatalf("%v %q", err, got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not arrive before the handler finished: the stream is held back")
	}
	close(release)
	rest, err := io.ReadAll(zr)
	if err != nil || string(rest) != "second chunk\n" {
		t.Fatalf("%v %q", err, rest)
	}
}
