package fastcgi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

func get(params ...Param) *Request { return &Request{Params: params} }

func readAll(t *testing.T, r *Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if err := r.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- protocol vectors, worked out by hand from the specification ----

func TestEncodeParamsVectors(t *testing.T) {
	got := encodeParams([]Param{{"A", "bc"}})
	if want := []byte{1, 2, 'A', 'b', 'c'}; !bytes.Equal(got, want) {
		t.Errorf("short pair: % x, want % x", got, want)
	}
	// 128 is the first length that needs the four-byte form with the top bit set.
	long := strings.Repeat("x", 128)
	got = encodeParams([]Param{{"N", long}})
	want := append([]byte{1, 0x80, 0, 0, 128, 'N'}, long...)
	if !bytes.Equal(got, want) {
		t.Errorf("long value: % x", got[:12])
	}
	// 127 still fits in one byte.
	got = encodeParams([]Param{{"N", strings.Repeat("x", 127)}})
	if got[1] != 127 {
		t.Errorf("127 must be a single byte, got % x", got[:3])
	}
}

func TestRecordFraming(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeRecord(bw, typeStdout, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	bw.Flush()
	// version 1, type 6, id 1, length 5, padding 3, reserved 0, content, padding.
	want := []byte{1, 6, 0, 1, 0, 5, 3, 0, 'h', 'e', 'l', 'l', 'o', 0, 0, 0}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("% x, want % x", buf.Bytes(), want)
	}
	// An empty record is just the header.
	buf.Reset()
	writeRecord(bw, typeParams, nil)
	bw.Flush()
	if want := []byte{1, 4, 0, 1, 0, 0, 0, 0}; !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("empty record: % x", buf.Bytes())
	}
}

func TestWriteStreamSplitsLargeData(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	data := bytes.Repeat([]byte("z"), 150000)
	if err := writeStream(bw, typeStdin, data); err != nil {
		t.Fatal(err)
	}
	bw.Flush()
	r := bufio.NewReader(&buf)
	var tmp []byte
	var got []byte
	records := 0
	for {
		typ, _, c, err := readRecord(r, &tmp)
		if err != nil {
			break
		}
		if typ != typeStdin || len(c) > maxContent {
			t.Fatalf("type %d length %d", typ, len(c))
		}
		got = append(got, c...)
		records++
	}
	if !bytes.Equal(got, data) || records != 3 {
		t.Errorf("round trip failed: %d bytes in %d records", len(got), records)
	}
}

// ---- requests ----

func TestGetRoundTrip(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		if r.Role != 1 {
			t.Errorf("role %d, want responder", r.Role)
		}
		r.Respond("Content-Type: text/plain\r\nX-Script: "+r.Params["SCRIPT_NAME"], "hi "+r.Params["QUERY_STRING"])
	})
	c := f.client()
	defer c.Close()
	resp, err := c.Do(context.Background(), get(Param{"SCRIPT_NAME", "/a.php"}, Param{"QUERY_STRING", "x=1"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || resp.Header.Get("Content-Type") != "text/plain" || resp.Header.Get("X-Script") != "/a.php" {
		t.Errorf("%d %v", resp.Status, resp.Header)
	}
	if got := readAll(t, resp); got != "hi x=1" {
		t.Errorf("body %q", got)
	}
}

func TestStatusHeader(t *testing.T) {
	cases := []struct {
		name, headers string
		status        int
		wantErr       bool
	}{
		{"default", "Content-Type: text/html", 200, false},
		{"explicit", "Status: 404 Not Found\r\nContent-Type: text/html", 404, false},
		{"code only", "Status: 503", 503, false},
		{"location alone is a redirect", "Location: /elsewhere", 302, false},
		{"location with status", "Status: 301 Moved\r\nLocation: /x", 301, false},
		{"junk", "Status: banana", 0, true},
		{"too low", "Status: 99 x", 0, true},
		{"too high", "Status: 600 x", 0, true},
		{"empty header block", "", 200, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, func(r *fakeReq) { r.Respond(tc.headers, "b") })
			c := f.client()
			defer c.Close()
			resp, err := c.Do(context.Background(), get())
			if tc.wantErr {
				if !errors.Is(err, ErrMalformedResponse) {
					t.Fatalf("want ErrMalformedResponse, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tc.status {
				t.Errorf("status %d, want %d", resp.Status, tc.status)
			}
			if resp.Header.Get("Status") != "" {
				t.Error("the Status header must not be passed on")
			}
			readAll(t, resp)
		})
	}
}

func TestLargeParamsAndBody(t *testing.T) {
	bigValue := strings.Repeat("v", 200000) // longer than one record, and a four-byte length
	body := bytes.Repeat([]byte("0123456789abcdef"), 40000)
	f := newFake(t, func(r *fakeReq) {
		in := r.Stdin()
		r.Stdout("Content-Type: application/octet-stream\r\nX-Big-Len: " + fmt.Sprint(len(r.Params["BIG"])) + "\r\n\r\n")
		r.Stdout(string(in))
		r.End(0, 0)
	})
	c := f.client()
	defer c.Close()
	resp, err := c.Do(context.Background(), &Request{
		Params: []Param{{"BIG", bigValue}, {"CONTENT_LENGTH", fmt.Sprint(len(body))}},
		Stdin:  bytes.NewReader(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Big-Len") != "200000" {
		t.Errorf("a long parameter arrived with length %q", resp.Header.Get("X-Big-Len"))
	}
	if got := readAll(t, resp); got != string(body) {
		t.Errorf("echoed body differs: %d bytes, want %d", len(got), len(body))
	}
}

func TestBadParametersAreRefused(t *testing.T) {
	f := newFake(t, func(r *fakeReq) { t.Error("a request with a bad parameter reached the backend") })
	c := f.client()
	defer c.Close()
	for name, p := range map[string]Param{
		"NUL in a value": {"SCRIPT_FILENAME", "/var/www/a.php\x00.jpg"},
		"NUL in a name":  {"A\x00B", "x"},
		"equals in name": {"A=B", "x"},
		"empty name":     {"", "x"},
	} {
		if _, err := c.Do(context.Background(), get(p)); !errors.Is(err, ErrBadParam) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ---- connection reuse ----

func TestConnectionsAreReused(t *testing.T) {
	f := newFake(t, func(r *fakeReq) { r.Respond("Content-Type: text/plain", "ok") })
	c := f.client()
	defer c.Close()
	for i := 0; i < 5; i++ {
		resp, err := c.Do(context.Background(), get())
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
	}
	if n := f.Accepted(); n != 1 {
		t.Errorf("%d connections for 5 sequential requests, want 1", n)
	}
}

func TestReuseDisabled(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		if r.KeepConn {
			t.Error("KEEP_CONN set although reuse is disabled")
		}
		r.Respond("", "ok")
	})
	c := f.client()
	c.MaxIdle = -1
	defer c.Close()
	for i := 0; i < 3; i++ {
		resp, err := c.Do(context.Background(), get())
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
	}
	if n := f.Accepted(); n != 3 {
		t.Errorf("%d connections, want 3", n)
	}
}

func TestAConnectionTheBackendClosedIsNotReused(t *testing.T) {
	// The backend answers and then drops the connection (as PHP-FPM does after
	// pm.max_requests) even though KEEP_CONN was asked for.
	f := newFake(t, func(r *fakeReq) {
		r.Respond("", "ok")
		r.Close()
	})
	c := f.client()
	defer c.Close()
	for i := 0; i < 4; i++ {
		resp, err := c.Do(context.Background(), get())
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if got := readAll(t, resp); got != "ok" {
			t.Fatalf("request %d: %q", i, got)
		}
		time.Sleep(30 * time.Millisecond) // let the close reach the idle watcher
	}
	if n := f.Accepted(); n != 4 {
		t.Errorf("%d connections, want 4 (a dead one must never be reused)", n)
	}
}

func TestIdleConnectionsExpire(t *testing.T) {
	f := newFake(t, func(r *fakeReq) { r.Respond("", "ok") })
	c := f.client()
	c.IdleTimeout = 20 * time.Millisecond
	defer c.Close()
	for i := 0; i < 2; i++ {
		resp, err := c.Do(context.Background(), get())
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
		time.Sleep(60 * time.Millisecond)
	}
	if n := f.Accepted(); n != 2 {
		t.Errorf("%d connections, want 2", n)
	}
}

func TestAnUnreadBodyDoesNotPoisonTheNextRequest(t *testing.T) {
	f := newFake(t, func(r *fakeReq) { r.Respond("", strings.Repeat("x", 100000)) })
	c := f.client()
	defer c.Close()
	resp, err := c.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Read(make([]byte, 10)) // read a little, then give up
	resp.Body.Close()
	resp, err = c.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, resp); len(got) != 100000 {
		t.Errorf("second response has %d bytes", len(got))
	}
}

// ---- failures ----

func TestNoOutputIsReportedWithStderr(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		r.Stdin()
		r.Stderr("PHP Fatal error: Uncaught Error in /var/www/a.php:3")
		r.End(255, 0)
	})
	c := f.client()
	defer c.Close()
	_, err := c.Do(context.Background(), get())
	if !errors.Is(err, ErrNoResponse) {
		t.Fatalf("got %v", err)
	}
	var se *StderrError
	if !errors.As(err, &se) || !strings.Contains(se.Stderr, "Uncaught Error") {
		t.Errorf("stderr was not carried: %#v", err)
	}
}

func TestStderrAfterHeadersIsAvailable(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		r.Stdin()
		r.Stdout("Content-Type: text/plain\r\n\r\npartial")
		r.Stderr("PHP Warning: something")
		r.End(0, 0)
	})
	c := f.client()
	defer c.Close()
	resp, err := c.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if !strings.Contains(resp.Stderr(), "PHP Warning") {
		t.Errorf("stderr %q", resp.Stderr())
	}
}

func TestBackendClosesBeforeAnswering(t *testing.T) {
	f := newFake(t, func(r *fakeReq) { r.Close() })
	c := f.client()
	defer c.Close()
	if _, err := c.Do(context.Background(), get()); !errors.Is(err, ErrBackendClosed) {
		t.Errorf("got %v", err)
	}
}

func TestBackendClosesMidBody(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		r.Stdin()
		r.Stdout("Content-Type: text/plain\r\n\r\nhalf of a resp")
		r.Close()
	})
	c := f.client()
	defer c.Close()
	resp, err := c.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !errors.Is(err, ErrBackendClosed) {
		t.Errorf("a truncated response must be an error, not a clean EOF: %v", err)
	}
	if string(b) != "half of a resp" {
		t.Errorf("body %q", b)
	}
}

func TestOverloaded(t *testing.T) {
	for name, status := range map[string]uint8{"overloaded": 2, "cant mpx": 1} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(r *fakeReq) { r.Stdin(); r.End(0, status) })
			c := f.client()
			defer c.Close()
			if _, err := c.Do(context.Background(), get()); !errors.Is(err, ErrOverloaded) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestGarbageFromTheBackend(t *testing.T) {
	cases := map[string]string{
		"wrong version":    "\x02\x06\x00\x01\x00\x00\x00\x00",
		"wrong request id": "\x01\x06\x00\x07\x00\x00\x00\x00",
		"plain http":       "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(r *fakeReq) { r.Conn().Write([]byte(raw)); r.Close() })
			c := f.client()
			defer c.Close()
			if _, err := c.Do(context.Background(), get()); err == nil {
				t.Error("garbage was accepted")
			}
		})
	}
}

func TestMalformedHeaders(t *testing.T) {
	cases := map[string]string{
		"no blank line":      "Content-Type: text/html\r\nstill headers",
		"a line without ':'": "this is not a header\r\n\r\nbody",
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(r *fakeReq) { r.Stdin(); r.Stdout(out); r.End(0, 0) })
			c := f.client()
			defer c.Close()
			if _, err := c.Do(context.Background(), get()); !errors.Is(err, ErrMalformedResponse) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestHeadersTooLarge(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		r.Stdin()
		r.Stdout("X-Big: " + strings.Repeat("a", 100000) + "\r\n\r\nbody")
		r.End(0, 0)
	})
	c := f.client()
	defer c.Close()
	if _, err := c.Do(context.Background(), get()); !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("got %v", err)
	}
}

func TestStderrIsCapped(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		r.Stdin()
		for i := 0; i < 20; i++ {
			r.Stderr(strings.Repeat("e", 30000))
		}
		r.Stdout("\r\nok")
		r.End(0, 0)
	})
	c := f.client()
	defer c.Close()
	resp, err := c.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if n := len(resp.Stderr()); n > maxStderrBytes+100 || !strings.Contains(resp.Stderr(), "truncated") {
		t.Errorf("stderr is %d bytes and not marked truncated", n)
	}
}

func TestConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c := &Client{Network: "tcp", Address: addr}
	if _, err := c.Do(context.Background(), get()); err == nil || !strings.Contains(err.Error(), "connecting to the backend") {
		t.Errorf("got %v", err)
	}
}

// ---- timeouts, cancellation, bodies ----

func TestBackendThatNeverAnswersTimesOut(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFake(t, func(r *fakeReq) { r.Stdin(); <-release })
	c := f.client()
	c.Timeout = 150 * time.Millisecond
	defer c.Close()
	start := time.Now()
	_, err := c.Do(context.Background(), get())
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

// slowReader delivers its data over several reads with pauses between them.
type slowReader struct {
	chunks int
	pause  time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.chunks == 0 {
		return 0, io.EOF
	}
	s.chunks--
	time.Sleep(s.pause)
	return copy(p, "chunk"), nil
}

func TestSlowUploadDoesNotCountAgainstTheBackend(t *testing.T) {
	f := newFake(t, func(r *fakeReq) { r.Respond("", fmt.Sprint(len(r.Stdin()))) })
	c := f.client()
	c.Timeout = 200 * time.Millisecond // the upload below takes 500 ms
	defer c.Close()
	resp, err := c.Do(context.Background(), &Request{Stdin: &slowReader{chunks: 5, pause: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, resp); got != "25" {
		t.Errorf("body %q", got)
	}
}

func TestCancellationAbortsTheRequest(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFake(t, func(r *fakeReq) { r.Stdin(); <-release })
	c := f.client()
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := c.Do(ctx, get())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

func TestCancellationDuringTheBody(t *testing.T) {
	proceed := make(chan struct{})
	f := newFake(t, func(r *fakeReq) {
		r.Stdin()
		r.Stdout("Content-Type: text/plain\r\n\r\nfirst")
		<-proceed
		r.Stdout("second")
		r.End(0, 0)
	})
	defer close(proceed)
	c := f.client()
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := c.Do(ctx, get())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

func TestFailingRequestBody(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFake(t, func(r *fakeReq) { r.Stdin(); <-release })
	c := f.client()
	defer c.Close()
	boom := errors.New("client went away")
	body := io.MultiReader(strings.NewReader("part"), &errReader{boom})
	_, err := c.Do(context.Background(), &Request{Stdin: body})
	var be *BodyError
	if !errors.As(err, &be) || !errors.Is(err, boom) {
		t.Errorf("want a BodyError wrapping the cause, got %v", err)
	}
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }

func TestBackendAnswersBeforeReadingTheBody(t *testing.T) {
	// A script that rejects the request without reading its (large) body.
	f := newFake(t, func(r *fakeReq) {
		r.Stdout("Status: 403 Forbidden\r\n\r\nno")
		r.End(0, 0)
		r.Drain()
	})
	c := f.client()
	defer c.Close()
	big := bytes.NewReader(make([]byte, 64<<20))
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := c.Do(context.Background(), &Request{Stdin: big})
		if err != nil {
			t.Errorf("Do: %v", err)
			return
		}
		if resp.Status != 403 {
			t.Errorf("status %d", resp.Status)
		}
		readAll(t, resp)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("hung while the backend answered without reading the body")
	}
}

// ---- concurrency ----

func TestMaxConnsLimitsTheBackendLoad(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		time.Sleep(20 * time.Millisecond)
		r.Respond("", "ok")
	})
	c := f.client()
	c.MaxConns = 3
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Do(context.Background(), get())
			if err != nil {
				t.Error(err)
				return
			}
			readAll(t, resp)
		}()
	}
	wg.Wait()
	if m := f.MaxActive(); m > 3 {
		t.Errorf("%d requests ran at once, limit is 3", m)
	}
}

func TestWaitingForAConnectionHonoursTheContext(t *testing.T) {
	hold := make(chan struct{})
	f := newFake(t, func(r *fakeReq) { r.Stdin(); <-hold; r.Respond("", "") })
	c := f.client()
	c.MaxConns = 1
	defer c.Close()
	defer close(hold)
	go func() {
		if resp, err := c.Do(context.Background(), get()); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Do(ctx, get()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v", err)
	}
}

func TestConcurrentRequestsStayApart(t *testing.T) {
	f := newFake(t, func(r *fakeReq) {
		in := r.Stdin()
		r.Respond("X-Id: "+r.Params["ID"], r.Params["ID"]+":"+string(in))
	})
	c := f.client()
	defer c.Close()
	var wg sync.WaitGroup
	for g := 0; g < 40; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				id := fmt.Sprintf("%d-%d", g, i)
				payload := strings.Repeat(id+",", 1+(g*i)%3000)
				resp, err := c.Do(context.Background(), &Request{
					Params: []Param{{"ID", id}},
					Stdin:  strings.NewReader(payload),
				})
				if err != nil {
					t.Error(err)
					return
				}
				b, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || string(b) != id+":"+payload || resp.Header.Get("X-Id") != id {
					t.Errorf("request %s got a different request's data (err %v)", id, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestUnixSocket(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir, err := tempSocketDir(t)
	if err != nil {
		t.Skip("no usable socket directory:", err)
	}
	ln, err := net.Listen("unix", dir+"/fcgi.sock")
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	defer ln.Close()
	fcgitest.NewOn(t, ln, func(r *fakeReq) { r.Respond("", "unix ok") })
	c := &Client{Network: "unix", Address: dir + "/fcgi.sock"}
	defer c.Close()
	resp, err := c.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, resp); got != "unix ok" {
		t.Errorf("%q", got)
	}
}

var _ = http.StatusOK

// A connection must be reused even when the scheduler runs the goroutine that
// sends the request late: the backend can answer before that goroutine has
// returned from its last write. This used to discard about one connection in 700.
func TestConnectionReuseDoesNotDependOnScheduling(t *testing.T) {
	if testing.Short() {
		t.Skip("a statistical test")
	}
	f := newFake(t, func(r *fakeReq) { r.Respond("", "ok") })
	c := f.client()
	defer c.Close()
	for i := 0; i < 1500; i++ {
		resp, err := c.Do(context.Background(), get())
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
	}
	if n := f.Accepted(); n != 1 {
		t.Errorf("%d connections for 1500 sequential requests, want 1", n)
	}
}
