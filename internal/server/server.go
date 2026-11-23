// Package server turns a compiled configuration into running HTTP and HTTPS
// listeners. It uses Go's net/http and crypto/tls for the protocols (HTTP/1.1,
// HTTP/2 over TLS) and adds virtual hosting, certificate selection by SNI,
// routing, static file serving, access logging, connection limits and
// configuration reload.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laikait/lotus/internal/clientip"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi"
	"github.com/laikait/lotus/internal/fspath"
	"github.com/laikait/lotus/internal/logging"
	"github.com/laikait/lotus/internal/staticfiles"
	"github.com/laikait/lotus/internal/vhost"
)

// Options configure a Server.
type Options struct {
	// Logger receives server events. Nil logs to stderr.
	Logger *slog.Logger
	// Stdout is where access logs go when a site has no log file. Nil is os.Stdout.
	Stdout io.Writer
	// Stderr is where error logs go when a site has no log file. Nil is os.Stderr.
	Stderr io.Writer
	// Now returns the current time, for certificate checks. Nil is time.Now.
	Now func() time.Time
}

// Server runs the listeners of one configuration, and can switch to a new
// configuration without dropping connections (Reload).
type Server struct {
	log     *slog.Logger
	opts    Options
	limiter *connLimiter
	// defaultAccess is shared by all generations so lines from the old and the
	// new one never interleave.
	defaultAccess *logging.Access

	gen    atomic.Pointer[generation]
	genSeq atomic.Uint64
	// tracker notices requests that net/http rejects before the handler runs.
	tracker *connTracker

	reloadMu sync.Mutex // one reload at a time

	// pools are the php-cgi process pools in use, shared by the generations
	// that ask for the same settings, so a reload that doesn't change them
	// neither restarts the processes nor runs two sets side by side.
	poolMu sync.Mutex
	pools  map[string]*sharedPool

	listeners []*boundListener

	errc chan error

	mu      sync.Mutex
	started bool
}

type boundListener struct {
	conf  model.Listener
	srv   *http.Server
	https bool
	ln    net.Listener
}

// New prepares a server: it opens document roots and log files, loads
// certificates and builds the request handlers. Nothing is bound to the
// network until Start.
func New(cfg *model.Config, opts Options) (*Server, error) {
	if items := Unsupported(cfg); len(items) > 0 {
		return nil, &UnsupportedError{Items: items}
	}
	if opts.Logger == nil {
		opts.Logger = logging.NewErrorLogger(os.Stderr, "server")
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	s := &Server{
		log:           opts.Logger,
		opts:          opts,
		limiter:       newConnLimiter(cfg.Limits.MaxConnections),
		defaultAccess: logging.NewAccess(opts.Stdout),
		errc:          make(chan error, len(cfg.Listeners)),
	}
	s.tracker = newConnTracker(s.log)
	g, err := s.buildGeneration(cfg)
	if err != nil {
		return nil, err
	}
	s.publish(g)

	for _, l := range cfg.Listeners {
		name := l.Name
		srv := &http.Server{
			Handler:           &listenerHandler{srv: s, listener: name},
			ReadHeaderTimeout: time.Duration(cfg.Limits.HeaderTimeout),
			ReadTimeout:       time.Duration(cfg.Limits.ReadTimeout),
			WriteTimeout:      time.Duration(cfg.Limits.WriteTimeout),
			IdleTimeout:       time.Duration(cfg.Limits.IdleTimeout),
			MaxHeaderBytes:    int(cfg.Limits.MaxHeaderBytes),
			ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
			ConnState:         s.tracker.hook,
			ConnContext:       s.tracker.connContext,
		}
		if l.HTTPS {
			srv.TLSConfig = newTLSConfig(cfg.TLS.MinVersion, func() *vhost.Table[*siteRuntime] {
				return s.gen.Load().tables[name]
			})
		}
		s.listeners = append(s.listeners, &boundListener{conf: l, srv: srv, https: l.HTTPS})
	}
	return s, nil
}

// acquire returns the current generation with a reference held, or nil if the
// server has shut down.
func (s *Server) acquire() *generation {
	for {
		g := s.gen.Load()
		if g.acquire() {
			return g
		}
		if s.gen.Load() == g {
			return nil // closed and not replaced: the server is shutting down
		}
	}
}

// publish makes g the current generation and numbers it. Numbers count the
// configurations that were actually used, so a failed build doesn't consume one.
func (s *Server) publish(g *generation) {
	g.number = s.genSeq.Add(1)
	s.gen.Store(g)
}

// buildGeneration opens everything cfg needs. On failure nothing stays open.
func (s *Server) buildGeneration(cfg *model.Config) (*generation, error) {
	g := &generation{
		cfg:      cfg,
		tables:   map[string]*vhost.Table[*siteRuntime]{},
		clients:  clientip.New(cfg.TrustedProxies),
		fallback: s.defaultAccess,
		php:      make(map[string]phpBackend, len(cfg.PHPBackends)),
	}
	for _, b := range cfg.PHPBackends {
		if b.Managed() {
			be, closer, err := s.managedPool(b)
			if err != nil {
				g.closeNow()
				return nil, fmt.Errorf("php_backend %q: %w", b.Name, err)
			}
			g.php[b.Name] = be
			g.closers = append(g.closers, closer)
			continue
		}
		c := &fastcgi.Client{Network: b.Network, Address: b.Address, Timeout: time.Duration(b.Timeout)}
		g.php[b.Name] = c
		g.closers = append(g.closers, poolCloser{c})
	}
	sites := make(map[string]*siteRuntime, len(cfg.Sites))
	for i := range cfg.Sites {
		rt, err := s.newSite(g, &cfg.Sites[i])
		if err != nil {
			g.closeNow()
			return nil, err
		}
		sites[cfg.Sites[i].Name] = rt
	}
	for _, l := range cfg.Listeners {
		var entries []vhost.Entry[*siteRuntime]
		for i := range cfg.Sites {
			site := &cfg.Sites[i]
			if !contains(site.Listeners, l.Name) {
				continue
			}
			entries = append(entries, vhost.Entry[*siteRuntime]{
				Value: sites[site.Name],
				Hosts: append([]string{site.Name}, site.Aliases...),
			})
		}
		g.tables[l.Name] = vhost.Build(entries)
	}
	return g, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) newSite(g *generation, site *model.Site) (*siteRuntime, error) {
	rt := &siteRuntime{
		site:    site,
		headers: site.SecurityHeaders,
		maxBody: site.Limits.MaxRequestBody,
		php:     g.php,
		phpSite: site.UsesPHP(),
	}

	access, err := s.logWriter(g, site.Logs.Access, s.opts.Stdout)
	if err != nil {
		return nil, fmt.Errorf("site %q: access log: %w", site.Name, err)
	}
	rt.access = logging.NewAccess(access)
	// The error log is opened (so a bad path fails at start) and used for the
	// handler's own error reports.
	errw, err := s.logWriter(g, site.Logs.Error, s.opts.Stderr)
	if err != nil {
		return nil, fmt.Errorf("site %q: error log: %w", site.Name, err)
	}
	siteLog := logging.NewErrorLogger(errw, site.Name)

	rt.log = siteLog
	rt.handlers = map[string]*staticfiles.Handler{}

	// One resolver per distinct document root, one handler per root and index list.
	resolvers := map[string]*fspath.Resolver{}
	addRoot := func(root string, index []string) error {
		if root == "" {
			return nil
		}
		key := filesKey(root, index)
		if rt.handlers[key] != nil {
			return nil
		}
		res := resolvers[root]
		if res == nil {
			var err error
			res, err = fspath.New(root, fspath.Policy{
				Symlinks:      symlinkPolicy(site.Symlinks),
				AllowDotfiles: site.Dotfiles == model.DotfilesAllow,
			})
			if err != nil {
				return fmt.Errorf("site %q: %w", site.Name, err)
			}
			resolvers[root] = res
			g.closers = append(g.closers, res)
		}
		rt.handlers[key] = staticfiles.New(res, staticfiles.Options{Index: index, Logger: siteLog})
		return nil
	}
	var walk func(routes []model.Route) error
	walk = func(routes []model.Route) error {
		for i := range routes {
			if err := addRoot(routes[i].Root, routes[i].Index); err != nil {
				return err
			}
			if err := walk(routes[i].Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := addRoot(site.Root, site.Index); err != nil {
		return nil, err
	}
	if err := walk(site.Routes); err != nil {
		return nil, err
	}
	if err := walk(site.Named); err != nil {
		return nil, err
	}
	rt.defaultFiles = rt.handlers[filesKey(site.Root, site.Index)]
	rt.resolvers = resolvers
	if site.HTAccess != nil {
		rt.ht = newHTRuntime(site.HTAccess, siteLog)
		rt.protected = map[string]bool{}
		for _, h := range site.SecurityHeaders {
			rt.protected[http.CanonicalHeaderKey(h.Name)] = true
		}
	}

	if site.TLS == model.TLSManual {
		cert, err := loadCertificate(site, s.log, s.opts.Now())
		if err != nil {
			return nil, err
		}
		rt.cert = cert
	}
	return rt, nil
}

func symlinkPolicy(p string) fspath.SymlinkPolicy {
	switch p {
	case model.SymlinksFollow:
		return fspath.SymlinksFollow
	case model.SymlinksOwnerMatch:
		return fspath.SymlinksOwnerMatch
	}
	return fspath.SymlinksDeny
}

// logWriter returns where a log goes: nowhere, a file, or the default stream.
func (s *Server) logWriter(g *generation, t model.LogTarget, def io.Writer) (io.Writer, error) {
	switch {
	case t.Off:
		return io.Discard, nil
	case t.Path != "":
		f, err := logging.OpenFile(t.Path)
		if err != nil {
			return nil, err
		}
		g.closers = append(g.closers, f)
		g.logFiles++
		return f, nil
	}
	return def, nil
}

// Start binds every listener and begins serving. If any listener can't be
// bound, nothing is left running and the error says which one.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("server already started")
	}
	for _, bl := range s.listeners {
		ln, err := net.Listen("tcp", bl.conf.Address)
		if err != nil {
			for _, other := range s.listeners {
				if other.ln != nil {
					other.ln.Close()
					other.ln = nil
				}
			}
			// A server that failed to start can't be started again, so release
			// the document roots and log files that New opened.
			s.gen.Load().closeNow()
			return fmt.Errorf("listener %q: %w", bl.conf.Name, err)
		}
		bl.ln = s.limiter.wrap(ln)
	}
	s.started = true
	for _, bl := range s.listeners {
		go s.serve(bl)
		kind := "http"
		if bl.https {
			kind = "https"
		}
		s.log.Info("listening", "listener", bl.conf.Name, "address", bl.ln.Addr().String(), "protocol", kind)
	}
	return nil
}

func (s *Server) serve(bl *boundListener) {
	var err error
	if bl.https {
		err = bl.srv.ServeTLS(bl.ln, "", "") // certificates come from TLSConfig.GetCertificate
	} else {
		err = bl.srv.Serve(bl.ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.errc <- fmt.Errorf("listener %q: %w", bl.conf.Name, err)
	}
}

// Errors delivers a listener's failure if it stops serving unexpectedly.
func (s *Server) Errors() <-chan error { return s.errc }

// Addrs returns the address each listener is bound to, by listener name.
// It is only meaningful after Start (the port may have been chosen by the OS).
func (s *Server) Addrs() map[string]net.Addr {
	out := map[string]net.Addr{}
	for _, bl := range s.listeners {
		if bl.ln != nil {
			out[bl.conf.Name] = bl.ln.Addr()
		}
	}
	return out
}

// DroppedRequests reports how many requests net/http rejected or saw abandoned
// before they reached a handler (malformed, too large, too slow).
func (s *Server) DroppedRequests() uint64 { return s.tracker.dropped.Load() }

// RejectedConnections reports how many connections were closed because the
// connection limit was reached.
func (s *Server) RejectedConnections() uint64 { return s.limiter.rejected.Load() }

// Shutdown stops accepting connections, lets running requests finish until ctx
// ends, then closes everything. It is safe to call more than once.
func (s *Server) Shutdown(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(s.listeners))
	for _, bl := range s.listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := bl.srv.Shutdown(ctx); err != nil {
				bl.srv.Close() // the deadline passed: drop what is left
				errs <- fmt.Errorf("listener %q: %w", bl.conf.Name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	s.reloadMu.Lock() // a reload in progress finishes first
	s.gen.Load().closeNow()
	s.reloadMu.Unlock()
	return errors.Join(drain(errs)...)
}

func drain(c <-chan error) []error {
	var out []error
	for e := range c {
		out = append(out, e)
	}
	return out
}
