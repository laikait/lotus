package logging

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var when = time.Date(2026, 10, 10, 13, 55, 36, 0, time.UTC)

func TestFormat(t *testing.T) {
	got := Format(Entry{
		Time: when, RemoteIP: "203.0.113.9", Method: "GET", URI: "/a?b=1", Proto: "HTTP/1.1",
		Status: 200, Bytes: 2326, Referer: "https://x.test/", UserAgent: "curl/8.0",
	})
	want := `203.0.113.9 - - [10/Oct/2026:13:55:36 +0000] "GET /a?b=1 HTTP/1.1" 200 2326 "https://x.test/" "curl/8.0"` + "\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestFormatEmptyFields(t *testing.T) {
	got := Format(Entry{Time: when, Method: "HEAD", URI: "/", Proto: "HTTP/2.0", Status: 304})
	want := `- - - [10/Oct/2026:13:55:36 +0000] "HEAD / HTTP/2.0" 304 - "-" "-"` + "\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestFormatUsesTimeZoneOffset(t *testing.T) {
	loc := time.FixedZone("x", 5*3600+30*60)
	got := Format(Entry{Time: when.In(loc), Method: "GET", URI: "/", Proto: "HTTP/1.1", Status: 200})
	if !strings.Contains(got, "[10/Oct/2026:19:25:36 +0530]") {
		t.Errorf("got %q", got)
	}
}

func TestEscape(t *testing.T) {
	tests := map[string]string{
		"plain /path?q=1": "plain /path?q=1",
		`say "hi"`:        `say \"hi\"`,
		`back\slash`:      `back\\slash`,
		"line1\nline2":    `line1\x0aline2`,
		"cr\rlf":          `cr\x0dlf`,
		"tab\there":       `tab\x09here`,
		"nul\x00byte":     `nul\x00byte`,
		"\x1b[31mred":     `\x1b[31mred`, // terminal escape sequence
		"del\x7f":         `del\x7f`,
		"caf\xc3\xa9":     `caf\xc3\xa9`, // UTF-8 is written byte by byte
		"bad\xffutf8":     `bad\xffutf8`,
		"":                "",
	}
	for in, want := range tests {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

// The point of escaping: whatever a client sends, one request is one line,
// and the quoted fields can't be closed early.
func TestLogInjectionIsNeutralized(t *testing.T) {
	var buf bytes.Buffer
	a := NewAccess(&buf)
	a.Log(Entry{
		Time: when, RemoteIP: "1.2.3.4", Method: "GET",
		URI:       "/x HTTP/1.1\" 200 0 \"-\" \"-\"\n9.9.9.9 - - [01/Jan/2000:00:00:00 +0000] \"GET /admin",
		Proto:     "HTTP/1.1",
		Status:    404,
		Referer:   "\"\nforged",
		UserAgent: "ua\r\n\x1b]0;title\x07",
	})
	out := buf.String()
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("one request must be one line, got %d:\n%q", n, out)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\r") {
		t.Errorf("control characters survived: %q", out)
	}
	if strings.Count(out, `"`) != strings.Count(out, `\"`)+6 {
		t.Errorf("an unescaped quote appears inside a field: %q", out)
	}
}

func TestAccessConcurrentWritesStayWholeLines(t *testing.T) {
	var buf bytes.Buffer
	a := NewAccess(&buf)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				a.Log(Entry{Time: when, RemoteIP: "1.1.1.1", Method: "GET", URI: "/" + strings.Repeat("x", 100), Proto: "HTTP/1.1", Status: 200, Bytes: 1})
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 1000 {
		t.Fatalf("got %d lines, want 1000", len(lines))
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "1.1.1.1 - - [") || !strings.HasSuffix(l, `"-" "-"`) {
			t.Fatalf("interleaved or damaged line: %q", l)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

func TestWriteFailureDoesNotPanicOrBlock(t *testing.T) {
	a := NewAccess(failingWriter{})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			a.Log(Entry{Time: when, Method: "GET", URI: "/", Proto: "HTTP/1.1", Status: 200})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging blocked on a failing writer")
	}
	if got := a.WriteErrors(); got != 10 {
		t.Errorf("WriteErrors = %d, want 10", got)
	}
}

func TestOpenFileAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	for i := 0; i < 2; i++ {
		f, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("line\n"))
		f.Close()
	}
	b, _ := os.ReadFile(path)
	if string(b) != "line\nline\n" {
		t.Errorf("file = %q; reopening must append", b)
	}
	if _, err := OpenFile(filepath.Join(t.TempDir(), "missing-dir", "x.log")); err == nil {
		t.Error("opening a log in a missing directory must fail")
	}
}

func TestErrorLoggerAddsComponent(t *testing.T) {
	var buf bytes.Buffer
	NewErrorLogger(&buf, "site-a").Error("boom", "path", "/x")
	out := buf.String()
	for _, want := range []string{"component=site-a", "msg=boom", "path=/x", "level=ERROR"} {
		if !strings.Contains(out, want) {
			t.Errorf("log line %q is missing %q", out, want)
		}
	}
}
