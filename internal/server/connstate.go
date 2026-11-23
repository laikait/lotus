package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// net/http answers some requests itself, before any handler runs: a malformed
// request line, a header section that is too large, a request that never
// finishes arriving. Those never reach the access log. The tracker notices
// them: a connection on which a request began (bytes arrived) but no handler
// ever ran for it, when the connection closes, is counted and reported.
//
// It can't tell which of the cases it was (net/http gives no hook for that),
// so the report says what is known: the peer and that the request was
// rejected or abandoned before it was routed.

type connInfo struct {
	// awaiting is true from the moment a request starts arriving until a
	// handler runs for it.
	awaiting atomic.Bool
}

type connInfoKey struct{}

type connTracker struct {
	log     *slog.Logger
	conns   sync.Map // net.Conn -> *connInfo
	dropped atomic.Uint64
	limit   logLimiter
}

func newConnTracker(log *slog.Logger) *connTracker {
	return &connTracker{log: log, limit: logLimiter{burst: 10, every: 200 * time.Millisecond}}
}

// hook is http.Server.ConnState.
func (t *connTracker) hook(c net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		t.conns.LoadOrStore(c, &connInfo{}) // connContext may have created it already
	case http.StateActive:
		if v, ok := t.conns.Load(c); ok {
			v.(*connInfo).awaiting.Store(true)
		}
	case http.StateClosed, http.StateHijacked:
		v, ok := t.conns.LoadAndDelete(c)
		if !ok {
			return
		}
		if state == http.StateClosed && v.(*connInfo).awaiting.Load() {
			t.dropped.Add(1)
			t.report(c)
		}
	}
}

// connContext is http.Server.ConnContext: it hands the connection's info to
// the handler so it can say a request arrived.
//
// net/http calls this before it reports StateNew, so it is where the info is
// created; the StateNew hook only makes sure it exists.
func (t *connTracker) connContext(ctx context.Context, c net.Conn) context.Context {
	v, _ := t.conns.LoadOrStore(c, &connInfo{})
	return context.WithValue(ctx, connInfoKey{}, v.(*connInfo))
}

// served is called by the handler: a request reached it.
func served(ctx context.Context) {
	if ci, ok := ctx.Value(connInfoKey{}).(*connInfo); ok {
		ci.awaiting.Store(false)
	}
}

func (t *connTracker) report(c net.Conn) {
	ok, suppressed := t.limit.allow()
	if !ok {
		return
	}
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		host = c.RemoteAddr().String()
	}
	attrs := []any{"remote", host}
	if suppressed > 0 {
		attrs = append(attrs, "similar_events_not_logged", suppressed)
	}
	t.log.Warn("a request was rejected or abandoned before it was routed (malformed, too large or too slow)", attrs...)
}

// logLimiter lets a burst of events through, then one per interval, and counts
// what it held back so the next event that is logged can say so. An attacker
// sending garbage must not be able to fill the log.
type logLimiter struct {
	burst int
	every time.Duration

	mu         sync.Mutex
	tokens     float64
	last       time.Time
	suppressed uint64
}

func (l *logLimiter) allow() (ok bool, suppressed uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.last.IsZero() {
		l.tokens = float64(l.burst)
	} else {
		l.tokens += float64(now.Sub(l.last)) / float64(l.every)
		if l.tokens > float64(l.burst) {
			l.tokens = float64(l.burst)
		}
	}
	l.last = now
	if l.tokens < 1 {
		l.suppressed++
		return false, 0
	}
	l.tokens--
	suppressed, l.suppressed = l.suppressed, 0
	return true, suppressed
}
