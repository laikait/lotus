package server

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/laikait/lotus/internal/ratelimit"
)

// connLimiter bounds the number of open connections across all listeners.
// A connection that arrives when the limit is reached is closed immediately.
type connLimiter struct {
	slots    chan struct{}
	rejected atomic.Uint64
	// perIP counts connections per client address (nil: no limit). Addresses
	// that exempt accepts (trusted proxies) are not counted: all their clients
	// share the one address.
	perIP      *ratelimit.PerIP
	exempt     func(netip.Addr) bool
	rejectedIP atomic.Uint64
}

func newConnLimiter(max, perIP int) *connLimiter {
	return &connLimiter{slots: make(chan struct{}, max), perIP: ratelimit.NewPerIP(perIP)}
}

// wrap returns a listener that enforces the limit.
func (c *connLimiter) wrap(l net.Listener) net.Listener {
	return &limitListener{Listener: l, lim: c}
}

type limitListener struct {
	net.Listener
	lim *connLimiter
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.lim.slots <- struct{}{}:
		default:
			l.lim.rejected.Add(1)
			conn.Close()
			continue
		}
		release := func() { <-l.lim.slots }
		if l.lim.perIP != nil {
			if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
				addr, _ := netip.AddrFromSlice(ta.IP)
				if l.lim.exempt == nil || !l.lim.exempt(addr.Unmap()) {
					free, ok := l.lim.perIP.Acquire(ratelimit.Key(addr))
					if !ok {
						<-l.lim.slots
						l.lim.rejectedIP.Add(1)
						conn.Close()
						continue
					}
					release = func() { free(); <-l.lim.slots }
				}
			}
		}
		return &limitConn{Conn: conn, release: release}, nil
	}
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// recorder wraps a ResponseWriter to remember the status and the number of
// body bytes for the access log. It keeps io.ReaderFrom so that file responses
// can still use sendfile, and Unwrap so http.ResponseController (flush,
// deadlines, hijack) still reaches the real writer.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// ReadFrom keeps the zero-copy path (sendfile) available.
func (r *recorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	rf, ok := r.ResponseWriter.(io.ReaderFrom)
	if !ok {
		return io.Copy(writerOnly{r}, src)
	}
	n, err := rf.ReadFrom(src)
	r.bytes += n
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// writerOnly hides ReadFrom so ioCopy can't recurse into recorder.ReadFrom.
type writerOnly struct{ w io.Writer }

func (w writerOnly) Write(p []byte) (int, error) { return w.w.Write(p) }
