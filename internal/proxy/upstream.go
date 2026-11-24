// Package proxy is LOTUS's reverse proxy: a pool of backends that requests are
// spread over, with health checking, retries, and TLS to the backends.
//
// An Upstream is an http.RoundTripper. The server wraps it in
// httputil.ReverseProxy, which handles the HTTP side (hop-by-hop headers,
// streaming, WebSocket upgrades); this package decides which backend gets a
// request and what to do when it fails.
package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Balancing methods.
const (
	RoundRobin = "round_robin" // smooth weighted round robin
	LeastConn  = "least_conn"  // fewest active requests relative to weight
	Random     = "random"      // power of two choices
	IPHash     = "ip_hash"     // the same client goes to the same backend
	URIHash    = "uri_hash"    // the same path goes to the same backend
)

// Errors a caller maps to a status.
var (
	// ErrNoBackend: every backend is down or excluded (503).
	ErrNoBackend = errors.New("proxy: no backend is available")
	// ErrBusy: every backend is at its connection limit (503).
	ErrBusy = errors.New("proxy: all backends are at their connection limit")
)

// BackendSpec is one backend of an upstream.
type BackendSpec struct {
	Network  string // "tcp" or "unix"
	Address  string // host:port, or a socket path
	TLS      bool   // https to this backend
	Host     string // host name for SNI and certificate checks (tcp)
	Weight   int
	MaxConns int // 0: unlimited
}

// HealthSpec configures active health checks.
type HealthSpec struct {
	Path               string // "" means a TCP connect check
	Interval, Timeout  time.Duration
	Healthy, Unhealthy int // consecutive results that change the state
}

// TLSSpec configures TLS to backends.
type TLSSpec struct {
	Verify     bool
	CAFile     string // extra trust roots (replaces the system pool)
	ServerName string // overrides the name checked and sent
	CertFile   string // client certificate
	KeyFile    string
}

// Config is an upstream.
type Config struct {
	Name     string
	Backends []BackendSpec
	Balance  string

	ConnectTimeout  time.Duration
	ResponseTimeout time.Duration // wait for the response headers
	IdleTimeout     time.Duration // idle pooled connections

	Retries     int // extra attempts on another backend
	MaxFails    int // failures within FailTimeout that take a backend out; 0 disables
	FailTimeout time.Duration
	MaxConns    int // active requests across the upstream; 0 unlimited

	Health *HealthSpec
	TLS    TLSSpec
}

// Upstream is a set of backends behind one name.
type Upstream struct {
	cfg      Config
	log      *slog.Logger
	backends []*backend
	active   atomic.Int64

	mu      sync.Mutex // guards the round-robin state of the backends
	counter uint64     // tie breaker for least_conn

	stop chan struct{}
	wg   sync.WaitGroup
}

type backend struct {
	spec  BackendSpec
	id    string // pool key and display name
	auth  string // host:port as written, for the Host header and health checks
	tr    *http.Transport
	state atomic.Int64 // connections in use

	current int // smooth round robin, guarded by Upstream.mu

	mu        sync.Mutex
	fails     int
	firstFail time.Time
	downUntil time.Time
	probeDown atomic.Bool // the active check says down
}

// New builds an upstream. It reads the TLS files and starts the health checks.
func New(cfg Config, log *slog.Logger) (*Upstream, error) {
	if len(cfg.Backends) == 0 {
		return nil, fmt.Errorf("upstream %q has no backends", cfg.Name)
	}
	if cfg.Balance == "" {
		cfg.Balance = RoundRobin
	}
	if cfg.FailTimeout <= 0 {
		cfg.FailTimeout = 10 * time.Second
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 90 * time.Second
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	tlsCfg, err := buildTLS(cfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("upstream %q: %w", cfg.Name, err)
	}
	u := &Upstream{cfg: cfg, log: log.With("upstream", cfg.Name), stop: make(chan struct{})}
	for i, s := range cfg.Backends {
		if s.Weight <= 0 {
			s.Weight = 1
		}
		if s.Host == "" && s.Network != "unix" {
			s.Host = hostOnly(s.Address)
		}
		b := &backend{spec: s, id: fmt.Sprintf("backend-%d", i), auth: s.Address}
		if s.Network == "unix" {
			b.auth = "localhost"
		}
		b.tr = u.transport(b, tlsCfg)
		u.backends = append(u.backends, b)
	}
	if cfg.Health != nil {
		for _, b := range u.backends {
			u.wg.Add(1)
			go u.healthLoop(b)
		}
	}
	return u, nil
}

func buildTLS(s TLSSpec) (*tls.Config, error) {
	c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName}
	if !s.Verify {
		c.InsecureSkipVerify = true //nolint:gosec // an explicit, documented choice of the administrator
	}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tls_ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls_ca: %s holds no certificate", s.CAFile)
		}
		c.RootCAs = pool
	}
	if s.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tls_client_cert: %w", err)
		}
		c.Certificates = []tls.Certificate{cert}
	}
	return c, nil
}

func (u *Upstream) transport(b *backend, base *tls.Config) *http.Transport {
	d := &net.Dialer{Timeout: u.cfg.ConnectTimeout, KeepAlive: 30 * time.Second}
	tc := base.Clone()
	if tc.ServerName == "" {
		tc.ServerName = b.spec.Host
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, b.spec.Network, b.spec.Address)
		},
		TLSClientConfig:       tc,
		ForceAttemptHTTP2:     b.spec.TLS,
		TLSHandshakeTimeout:   u.cfg.ConnectTimeout,
		ResponseHeaderTimeout: u.cfg.ResponseTimeout,
		IdleConnTimeout:       u.cfg.IdleTimeout,
		MaxIdleConnsPerHost:   64,
		ExpectContinueTimeout: time.Second,
		// The response goes to the client as the backend sent it: no automatic
		// gzip (which would also change what the client asked for).
		DisableCompression: true,
		Proxy:              nil,
	}
}

// Close stops the health checks and closes idle connections.
func (u *Upstream) Close() {
	select {
	case <-u.stop:
		return
	default:
		close(u.stop)
	}
	u.wg.Wait()
	for _, b := range u.backends {
		b.tr.CloseIdleConnections()
	}
}

// ---- state ----

func (b *backend) usable(now time.Time) bool {
	if b.probeDown.Load() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !now.Before(b.downUntil)
}

func (b *backend) full() bool {
	return b.spec.MaxConns > 0 && b.state.Load() >= int64(b.spec.MaxConns)
}

// failed records a failure for passive health checking.
func (u *Upstream) failed(b *backend, now time.Time) {
	if u.cfg.MaxFails <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fails == 0 || now.Sub(b.firstFail) > u.cfg.FailTimeout {
		b.fails, b.firstFail = 0, now
	}
	if b.fails++; b.fails >= u.cfg.MaxFails {
		b.downUntil = now.Add(u.cfg.FailTimeout)
		b.fails = 0
		u.log.Warn("backend taken out after failures", "backend", b.spec.Address, "failures", u.cfg.MaxFails, "for", u.cfg.FailTimeout)
	}
}

func (b *backend) succeeded() {
	b.mu.Lock()
	b.fails = 0
	b.mu.Unlock()
}

// ---- status ----

// BackendStatus describes a backend for `lotus status`.
type BackendStatus struct {
	Address string
	Healthy bool
	Active  int64
	Weight  int
}

// Status returns the state of every backend.
func (u *Upstream) Status() []BackendStatus {
	now := time.Now()
	out := make([]BackendStatus, len(u.backends))
	for i, b := range u.backends {
		out[i] = BackendStatus{Address: b.spec.Address, Healthy: b.usable(now), Active: b.state.Load(), Weight: b.spec.Weight}
	}
	return out
}

// ---- round trip ----

// RoundTrip sends the request to a backend and, when that fails in a way that
// is safe to repeat, to another one.
//
// A request is repeated only if nothing of it could have been acted on: the
// connection could not be made (the body was never read), or the method is
// idempotent and has no body. A POST that failed halfway is never sent twice.
func (u *Upstream) RoundTrip(req *http.Request) (*http.Response, error) {
	if m := u.cfg.MaxConns; m > 0 && u.active.Load() >= int64(m) {
		return nil, ErrBusy
	}
	body := newReplayBody(req.Body)

	tried := make(map[*backend]bool, len(u.backends))
	var lastErr error
	for attempt := 0; ; attempt++ {
		b, err := u.pick(req, tried)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		tried[b] = true

		out := *req
		uu := *req.URL
		uu.Host = b.id
		uu.Scheme = "http"
		if b.spec.TLS {
			uu.Scheme = "https"
		}
		out.URL = &uu
		if out.Host == "" {
			out.Host = b.auth
		}
		if body != nil {
			out.Body = body
		}

		b.state.Add(1)
		u.active.Add(1)
		resp, err := b.tr.RoundTrip(&out)
		if err == nil {
			b.succeeded()
			resp.Body = &countedBody{ReadCloser: resp.Body, done: func() { b.state.Add(-1); u.active.Add(-1) }}
			return resp, nil
		}
		b.state.Add(-1)
		u.active.Add(-1)

		if req.Context().Err() != nil {
			return nil, err // the client went away; the backend did nothing wrong
		}
		u.failed(b, time.Now())
		lastErr = err
		var safe bool
		if body != nil {
			safe = isDialError(err) && body.untouched()
		} else {
			safe = isDialError(err) || (isIdempotent(req.Method) && !isTimeout(err))
		}
		if !safe || attempt >= u.cfg.Retries {
			return nil, err
		}
	}
}

func isIdempotent(m string) bool {
	switch m {
	case "", http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// countedBody releases the backend's connection slot when the response ends.
type countedBody struct {
	io.ReadCloser
	done func()
	once sync.Once
}

func (c *countedBody) Close() error {
	c.once.Do(c.done)
	return c.ReadCloser.Close()
}

// Write is here so an upgraded connection (WebSocket) keeps working: the
// transport hands back a body that is also a writer for 101 responses.
func (c *countedBody) Write(p []byte) (int, error) {
	if w, ok := c.ReadCloser.(io.Writer); ok {
		return w.Write(p)
	}
	return 0, errors.New("proxy: response body is not writable")
}

// replayBody lets a request body survive a failed attempt: closing it does
// nothing (the transport closes bodies even when it never read them), and it
// remembers whether anything was read.
type replayBody struct {
	r    io.ReadCloser
	read atomic.Int64
}

func newReplayBody(b io.ReadCloser) *replayBody {
	if b == nil || b == http.NoBody {
		return nil
	}
	return &replayBody{r: b}
}

func (b *replayBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read.Add(int64(n))
	return n, err
}
func (b *replayBody) Close() error    { return nil }
func (b *replayBody) untouched() bool { return b.read.Load() == 0 }

// hostOnly splits "host:port" and returns the host.
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return strings.Trim(addr, "[]")
}
