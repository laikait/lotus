// Package server turns a compiled configuration into running HTTP and HTTPS
// listeners. It uses Go's net/http and crypto/tls for the protocols (HTTP/1.1,
// HTTP/2 over TLS) and adds virtual hosting, certificate selection by SNI,
// static file serving, access logging and connection limits.
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
	"time"

	"github.com/laikait/lotus/internal/config/model"
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

// Server runs the listeners of one configuration.
type Server struct {
	cfg     *model.Config
	log     *slog.Logger
	limiter *connLimiter

	listeners []*boundListener
	closers   []io.Closer // log files and document roots

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
		cfg:     cfg,
		log:     opts.Logger,
		limiter: newConnLimiter(cfg.Limits.MaxConnections),
		errc:    make(chan error, len(cfg.Listeners)),
	}
	fail := func(err error) (*Server, error) {
		s.closeResources()
		return nil, err
	}

	sites := make(map[string]*siteRuntime, len(cfg.Sites))
	for i := range cfg.Sites {
		rt, err := s.newSite(&cfg.Sites[i], opts)
		if err != nil {
			return fail(err)
		}
		sites[cfg.Sites[i].Name] = rt
	}

	defaultAccess := logging.NewAccess(opts.Stdout)
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
		table := vhost.Build(entries)

		srv := &http.Server{
			Handler:           &listenerHandler{table: table, fallback: defaultAccess},
			ReadHeaderTimeout: time.Duration(cfg.Limits.HeaderTimeout),
			ReadTimeout:       time.Duration(cfg.Limits.ReadTimeout),
			WriteTimeout:      time.Duration(cfg.Limits.WriteTimeout),
			IdleTimeout:       time.Duration(cfg.Limits.IdleTimeout),
			MaxHeaderBytes:    int(cfg.Limits.MaxHeaderBytes),
			ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		}
		if l.HTTPS {
			srv.TLSConfig = newTLSConfig(cfg.TLS.MinVersion, table)
		}
		s.listeners = append(s.listeners, &boundListener{conf: l, srv: srv, https: l.HTTPS})
	}
	return s, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) newSite(site *model.Site, opts Options) (*siteRuntime, error) {
	rt := &siteRuntime{
		site:    site,
		headers: site.SecurityHeaders,
		maxBody: site.Limits.MaxRequestBody,
	}

	access, err := s.logWriter(site.Logs.Access, opts.Stdout)
	if err != nil {
		return nil, fmt.Errorf("site %q: access log: %w", site.Name, err)
	}
	rt.access = logging.NewAccess(access)
	// The error log is opened (so a bad path fails at start) and used for the
	// handler's own error reports.
	errw, err := s.logWriter(site.Logs.Error, opts.Stderr)
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
			s.closers = append(s.closers, res)
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

	if site.TLS == model.TLSManual {
		cert, err := loadCertificate(site, s.log, opts.Now())
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
func (s *Server) logWriter(t model.LogTarget, def io.Writer) (io.Writer, error) {
	switch {
	case t.Off:
		return io.Discard, nil
	case t.Path != "":
		f, err := logging.OpenFile(t.Path)
		if err != nil {
			return nil, err
		}
		s.closers = append(s.closers, f)
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
			s.closeResources()
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
	s.closeResources()
	return errors.Join(drain(errs)...)
}

func drain(c <-chan error) []error {
	var out []error
	for e := range c {
		out = append(out, e)
	}
	return out
}

func (s *Server) closeResources() {
	for _, c := range s.closers {
		c.Close()
	}
	s.closers = nil
}
