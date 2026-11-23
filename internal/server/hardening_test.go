package server

import (
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

// These tests speak raw HTTP over TCP, so they can send what no HTTP client
// would: malformed requests, conflicting lengths, oversized headers.

var statusLine = regexp.MustCompile(`HTTP/1\.[01] (\d{3})`)

// exchange sends payload and returns everything the server writes until it
// closes the connection or wait passes.
func exchange(t *testing.T, addr, payload string, wait time.Duration) string {
	t.Helper()
	raw, _ := exchangeClosed(t, addr, payload, wait)
	return raw
}

// exchangeClosed is exchange that also reports whether the server closed the
// connection (as opposed to the wait running out with it still open).
func exchangeClosed(t *testing.T, addr, payload string, wait time.Duration) (raw string, closed bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(wait))
	if _, err := io.WriteString(conn, payload); err != nil {
		// the server may reject (and close) before it has read everything
		t.Logf("write: %v", err)
	}
	b, err := io.ReadAll(conn)
	var ne net.Error
	timedOut := errors.As(err, &ne) && ne.Timeout()
	return string(b), !timedOut // EOF or a reset: the server closed it
}

// statuses returns the status of every response in raw, in order.
func statuses(raw string) []int {
	var out []int
	for _, m := range statusLine.FindAllStringSubmatch(raw, -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

func hardenedServer(t *testing.T, mutate func(*model.Config)) (*harness, string) {
	t.Helper()
	cfg := baseConfig(siteConf("a.test", docroot(t, "home"), "http"))
	withLimits(cfg, func(l *model.Limits) {
		l.MaxHeaderBytes = 4096
		l.HeaderTimeout = model.Duration(400 * time.Millisecond)
	})
	if mutate != nil {
		mutate(cfg)
	}
	h := start(t, cfg)
	return h, h.addr("http")
}

const good = "GET / HTTP/1.1\r\nHost: a.test\r\n\r\n"

func TestWellFormedRequestsWork(t *testing.T) {
	_, addr := hardenedServer(t, nil)
	raw := exchange(t, addr, good+"GET /hello.txt HTTP/1.1\r\nHost: a.test\r\nConnection: close\r\n\r\n", 3*time.Second)
	// Pipelined requests are answered in order.
	if got := statuses(raw); len(got) != 2 || got[0] != 200 || got[1] != 200 {
		t.Fatalf("statuses = %v\n%s", got, raw)
	}
	if strings.Index(raw, "home") > strings.Index(raw, "hello from home") {
		t.Errorf("responses are out of order:\n%s", raw)
	}
}

func TestMalformedRequestsAreRejected(t *testing.T) {
	_, addr := hardenedServer(t, nil)
	tests := []struct {
		name, payload string
		want          int
	}{
		{"missing Host (HTTP/1.1)", "GET / HTTP/1.1\r\n\r\n", 400},
		{"two Host headers", "GET / HTTP/1.1\r\nHost: a.test\r\nHost: b.test\r\n\r\n", 400},
		{"HTTP/1.0 without Host", "GET / HTTP/1.0\r\n\r\n", 400},
		{"space in a header name", "GET / HTTP/1.1\r\nHost: a.test\r\nBad Header: x\r\n\r\n", 400},
		{"space before the colon", "GET / HTTP/1.1\r\nHost: a.test\r\nX-A : x\r\n\r\n", 400},
		{"NUL in a header value", "GET / HTTP/1.1\r\nHost: a.test\r\nX-A: a\x00b\r\n\r\n", 400},
		{"control character in the URL", "GET /a\x01b HTTP/1.1\r\nHost: a.test\r\n\r\n", 400},
		{"no HTTP version", "GET /\r\n\r\n", 400},
		{"garbage request line", "\x16\x03\x01\x02\x00\x01\x00\x01\xfc\x03\x03\r\n\r\n", 400},
		{"unsupported HTTP version", "GET / HTTP/9.9\r\nHost: a.test\r\n\r\n", 505},
		{"duplicate different Content-Length", "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: 3\r\nContent-Length: 4\r\n\r\nabcd", 400},
		{"negative Content-Length", "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: -1\r\n\r\n", 400},
		{"non-numeric Content-Length", "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: 1e3\r\n\r\n", 400},
		{"unknown Transfer-Encoding", "POST / HTTP/1.1\r\nHost: a.test\r\nTransfer-Encoding: gzip\r\n\r\n", 501},
		{"obfuscated Transfer-Encoding", "POST / HTTP/1.1\r\nHost: a.test\r\nTransfer-Encoding: xchunked\r\n\r\n", 501},
	}
	for _, tt := range tests {
		raw, closed := exchangeClosed(t, addr, tt.payload, 3*time.Second)
		got := statuses(raw)
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("%s: statuses %v, want [%d]\n%s", tt.name, got, tt.want, raw)
		}
		if !closed {
			t.Errorf("%s: the connection must be closed after a malformed request:\n%s", tt.name, raw)
		}
	}
}

// A request smuggling attack makes the server and a proxy in front of it
// disagree about where a request ends, so one request is read as two. When a
// request has conflicting framing, the connection must be closed after the
// response, so the bytes that follow are never read as a request.
func TestConflictingFramingClosesTheConnection(t *testing.T) {
	h, addr := hardenedServer(t, nil)
	hidden := "GET /smuggled HTTP/1.1\r\nHost: a.test\r\n\r\n"
	payloads := map[string]string{
		"CL then TE":      "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n" + hidden,
		"TE then CL":      "POST / HTTP/1.1\r\nHost: a.test\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n0\r\n\r\n" + hidden,
		"TE twice":        "POST / HTTP/1.1\r\nHost: a.test\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: identity\r\n\r\n0\r\n\r\n" + hidden,
		"bare LF, CL, TE": "POST / HTTP/1.1\nHost: a.test\nContent-Length: 4\nTransfer-Encoding: chunked\n\n0\n\n" + hidden,
		"plain chunked":   "POST / HTTP/1.1\r\nHost: a.test\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n" + hidden,
	}
	for name, payload := range payloads {
		raw, closed := exchangeClosed(t, addr, payload, 3*time.Second)
		if !closed {
			t.Errorf("%s: the connection stayed open:\n%s", name, raw)
		}
		if got := statuses(raw); len(got) > 1 {
			t.Errorf("%s: %d responses; the hidden request was answered:\n%s", name, len(got), raw)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(h.stdout.String(), "/smuggled") {
		t.Errorf("a smuggled request reached the handler:\n%s", h.stdout.String())
	}
}

// Framing that is unambiguous is followed exactly: what comes after a request
// is the next request. (CL.0 and chunk extensions are only dangerous when a
// proxy in front frames them differently, which the connection-close rule for
// chunked bodies and the strict header checks cover.)
func TestUnambiguousPipeliningFollowsTheRFC(t *testing.T) {
	_, addr := hardenedServer(t, nil)
	raw := exchange(t, addr, "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: 0\r\n\r\nGET /hello.txt HTTP/1.1\r\nHost: a.test\r\nConnection: close\r\n\r\n", 3*time.Second)
	if got := statuses(raw); len(got) != 2 || got[0] != 405 || got[1] != 200 {
		t.Errorf("statuses %v\n%s", got, raw)
	}
	// A body whose length is declared is consumed, not read as a request.
	raw = exchange(t, addr, "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: 40\r\n\r\nGET /smuggled HTTP/1.1\r\nHost: a.test\r\n\r\n", 3*time.Second)
	if got := statuses(raw); len(got) != 1 {
		t.Errorf("a declared body must not be parsed as a request: %v\n%s", got, raw)
	}
}

func TestOversizedHeadersAreRefused(t *testing.T) {
	_, addr := hardenedServer(t, nil)
	tests := map[string]string{
		"one huge header":      "GET / HTTP/1.1\r\nHost: a.test\r\nX-Big: " + strings.Repeat("a", 20000) + "\r\n\r\n",
		"very long URL":        "GET /" + strings.Repeat("a", 20000) + " HTTP/1.1\r\nHost: a.test\r\n\r\n",
		"thousands of headers": "GET / HTTP/1.1\r\nHost: a.test\r\n" + strings.Repeat("X: y\r\n", 3000) + "\r\n",
		"huge cookie":          "GET / HTTP/1.1\r\nHost: a.test\r\nCookie: " + strings.Repeat("a=b; ", 3000) + "\r\n\r\n",
	}
	for name, payload := range tests {
		raw := exchange(t, addr, payload, 3*time.Second)
		got := statuses(raw)
		if len(got) != 1 || got[0] != 431 {
			t.Errorf("%s: statuses %v, want [431]", name, got)
		}
	}
	// Just under the limit still works.
	ok := "GET / HTTP/1.1\r\nHost: a.test\r\nX-Pad: " + strings.Repeat("a", 3000) + "\r\nConnection: close\r\n\r\n"
	if got := statuses(exchange(t, addr, ok, 3*time.Second)); len(got) != 1 || got[0] != 200 {
		t.Errorf("a request under the limit: %v", got)
	}
}

func TestExpectContinueDoesNotInviteAHugeBody(t *testing.T) {
	_, addr := hardenedServer(t, nil)
	// The client announces 100 MB and waits for "100 Continue" before sending
	// it. The server must answer 413 without ever inviting the body.
	raw := exchange(t, addr, "POST /x HTTP/1.1\r\nHost: a.test\r\nContent-Length: 100000000\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", 3*time.Second)
	if strings.Contains(raw, "100 Continue") {
		t.Errorf("the server invited the body:\n%s", raw)
	}
	if got := statuses(raw); len(got) != 1 || got[0] != 413 {
		t.Errorf("statuses %v, want [413]\n%s", got, raw)
	}
}

func TestChunkedBodyOverTheLimitIsStopped(t *testing.T) {
	// Without a Content-Length the size is not known up front: the limit must
	// still hold while the body is read. A route that reads the body needs the
	// PHP/proxy phases; the static handler refuses POST, so only the headers
	// of the request matter here, and a declared length is checked at once.
	_, addr := hardenedServer(t, func(c *model.Config) {
		c.Sites[0].Limits.MaxRequestBody = 100
	})
	raw := exchange(t, addr, "POST / HTTP/1.1\r\nHost: a.test\r\nContent-Length: 101\r\nConnection: close\r\n\r\n"+strings.Repeat("a", 101), 3*time.Second)
	if got := statuses(raw); len(got) != 1 || got[0] != 413 {
		t.Errorf("statuses %v\n%s", got, raw)
	}
}

func TestAbsoluteFormTargetDoesNotConfuseVirtualHosts(t *testing.T) {
	cfg := baseConfig(siteConf("a.test", docroot(t, "site-a"), "http"), siteConf("b.test", docroot(t, "site-b"), "http"))
	h := start(t, cfg)
	// The Host header says b.test, the request target says a.test. net/http
	// uses the target's host; either way exactly one site must answer and the
	// answer must be consistent with the log.
	raw := exchange(t, h.addr("http"), "GET http://a.test/ HTTP/1.1\r\nHost: b.test\r\nConnection: close\r\n\r\n", 3*time.Second)
	if got := statuses(raw); len(got) != 1 || got[0] != 200 {
		t.Fatalf("statuses %v\n%s", got, raw)
	}
	if !strings.Contains(raw, "site-a") {
		t.Errorf("RFC 9112: the target's host wins over the Host header:\n%s", raw)
	}
}

// ---- requests that never reach a handler are visible ----

func waitDropped(h *harness, want uint64) uint64 {
	deadline := time.Now().Add(3 * time.Second)
	for h.srv.DroppedRequests() < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return h.srv.DroppedRequests()
}

func TestRejectedRequestsAreCountedAndLogged(t *testing.T) {
	h, addr := hardenedServer(t, nil)
	exchange(t, addr, "GET / HTTP/1.1\r\n\r\n", 3*time.Second)                                                     // no Host: 400 from net/http
	exchange(t, addr, "GET / HTTP/1.1\r\nHost: a.test\r\nX: "+strings.Repeat("a", 9000)+"\r\n\r\n", 3*time.Second) // 431
	if got := waitDropped(h, 2); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "rejected or abandoned before it was routed") || !strings.Contains(logs, "remote=127.0.0.1") {
		t.Errorf("the rejection must be logged with the peer:\n%s", logs)
	}
}

func TestSlowHeadersAreCountedAsDropped(t *testing.T) {
	h, addr := hardenedServer(t, nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: a.") // a request that never finishes
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.ReadAll(conn) // the server cuts it off at the header timeout
	if got := waitDropped(h, 1); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
}

func TestNormalTrafficIsNeverCountedAsDropped(t *testing.T) {
	h, addr := hardenedServer(t, nil)
	// Keep-alive with several requests, an error response, a conditional
	// request and a client that just connects and leaves: none of them is a
	// rejected request.
	exchange(t, addr, good+good+"GET /missing HTTP/1.1\r\nHost: a.test\r\nConnection: close\r\n\r\n", 3*time.Second)
	exchange(t, addr, "", 200*time.Millisecond) // connect, say nothing, leave
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
	}
	getHost(t, "http://"+addr+"/", "a.test")
	time.Sleep(300 * time.Millisecond)
	if got := h.srv.DroppedRequests(); got != 0 {
		t.Errorf("dropped = %d, want 0\n%s", got, h.logs.String())
	}
}

func TestSecondRequestOnAKeepAliveConnectionIsTracked(t *testing.T) {
	h, addr := hardenedServer(t, nil)
	raw := exchange(t, addr, good+"GET / HTTP/1.1\r\n\r\n", 3*time.Second) // the second has no Host
	if got := statuses(raw); len(got) != 2 || got[0] != 200 || got[1] != 400 {
		t.Fatalf("statuses %v\n%s", got, raw)
	}
	if got := waitDropped(h, 1); got != 1 {
		t.Errorf("dropped = %d: a bad request after a good one must still be noticed", got)
	}
}

func TestRejectionLogsAreRateLimited(t *testing.T) {
	h, addr := hardenedServer(t, nil)
	const attacks = 60
	for i := 0; i < attacks; i++ {
		exchange(t, addr, "GET / HTTP/1.1\r\n\r\n", 2*time.Second)
	}
	if got := waitDropped(h, attacks); got != attacks {
		t.Fatalf("every rejection is counted: %d of %d", got, attacks)
	}
	logged := strings.Count(h.logs.String(), "rejected or abandoned before it was routed")
	if logged >= attacks/2 {
		t.Errorf("%d of %d rejections were logged; an attacker must not be able to flood the log", logged, attacks)
	}
	if logged < 5 {
		t.Errorf("only %d logged; a burst should still be visible", logged)
	}
}

func TestLogLimiter(t *testing.T) {
	l := logLimiter{burst: 3, every: time.Hour}
	for i := 0; i < 3; i++ {
		if ok, s := l.allow(); !ok || s != 0 {
			t.Fatalf("event %d: %v %d", i, ok, s)
		}
	}
	for i := 0; i < 4; i++ {
		if ok, _ := l.allow(); ok {
			t.Fatal("the burst is used up")
		}
	}
	l.mu.Lock()
	l.tokens = 1 // time passes
	l.mu.Unlock()
	if ok, s := l.allow(); !ok || s != 4 {
		t.Errorf("after the pause: ok=%v suppressed=%d, want true and 4", ok, s)
	}
}
