// Package logging writes access logs (Combined Log Format) and builds the
// structured error logger.
//
// Everything that comes from a request is escaped before it is written, so a
// client can't inject extra log lines or terminal escape sequences.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is one finished request.
type Entry struct {
	Time      time.Time
	RemoteIP  string
	Method    string
	URI       string // the request target as received, including the query
	Proto     string
	Status    int
	Bytes     int64
	Referer   string
	UserAgent string
}

// Access writes entries in Combined Log Format:
//
//	203.0.113.9 - - [10/Oct/2026:13:55:36 +0000] "GET /a?b=1 HTTP/1.1" 200 2326 "https://x.test/" "curl/8.0"
//
// It is safe for concurrent use. Writes are serialized; a failing write (for
// example a full disk) never blocks or fails a request, it is only counted.
type Access struct {
	mu     sync.Mutex
	w      io.Writer
	errors atomic.Uint64
}

// NewAccess returns a logger that writes to w.
func NewAccess(w io.Writer) *Access { return &Access{w: w} }

// Log writes one entry.
func (a *Access) Log(e Entry) {
	line := Format(e)
	a.mu.Lock()
	_, err := io.WriteString(a.w, line)
	a.mu.Unlock()
	if err != nil {
		a.errors.Add(1)
	}
}

// WriteErrors reports how many entries could not be written.
func (a *Access) WriteErrors() uint64 { return a.errors.Load() }

// Format renders an entry as one newline-terminated Combined Log Format line.
func Format(e Entry) string {
	var b strings.Builder
	b.Grow(160)
	b.WriteString(dash(Escape(e.RemoteIP)))
	b.WriteString(" - - [")
	b.WriteString(e.Time.Format("02/Jan/2006:15:04:05 -0700"))
	b.WriteString(`] "`)
	b.WriteString(Escape(e.Method))
	b.WriteByte(' ')
	b.WriteString(Escape(e.URI))
	b.WriteByte(' ')
	b.WriteString(Escape(e.Proto))
	b.WriteString(`" `)
	b.WriteString(strconv.Itoa(e.Status))
	b.WriteByte(' ')
	if e.Bytes > 0 {
		b.WriteString(strconv.FormatInt(e.Bytes, 10))
	} else {
		b.WriteByte('-')
	}
	b.WriteString(` "`)
	b.WriteString(dashEmpty(Escape(e.Referer)))
	b.WriteString(`" "`)
	b.WriteString(dashEmpty(Escape(e.UserAgent)))
	b.WriteString("\"\n")
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func dashEmpty(s string) string { return dash(s) }

// Escape makes a request-derived value safe to put between quotes in a log
// line: the quote and the backslash are backslash-escaped, and every byte
// outside printable ASCII (control characters, DEL, and all non-ASCII bytes)
// is written as \xNN.
func Escape(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c > 0x7e:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// OpenFile opens (creating if needed) a log file for appending.
func OpenFile(path string) (io.WriteCloser, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	return f, nil
}

// NewErrorLogger returns a structured logger. component is added to every
// record (for example "server" or a site name).
func NewErrorLogger(w io.Writer, component string) *slog.Logger {
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With("component", component)
}
