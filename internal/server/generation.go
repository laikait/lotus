package server

import (
	"io"
	"sort"
	"sync"
	"time"

	"github.com/laikait/lotus/internal/cache"
	"github.com/laikait/lotus/internal/clientip"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fastcgi"
	"github.com/laikait/lotus/internal/logging"
	"github.com/laikait/lotus/internal/phpproc"
	"github.com/laikait/lotus/internal/proxy"
	"github.com/laikait/lotus/internal/vhost"
)

// generation is everything a configuration needs at request time: the sites,
// the virtual host table of each listener, the client IP rules, and the files
// and log handles they hold. A generation never changes after it is built.
// Reload builds a new one beside the old and swaps it in atomically, so a
// request sees one consistent configuration from start to finish.
type generation struct {
	number   uint64 // 1 for the first configuration, +1 for each reload
	logFiles int    // log files this generation opened
	cfg      *model.Config
	tables   map[string]*vhost.Table[*siteRuntime] // by listener name
	clients  *clientip.Resolver
	fallback *logging.Access       // access log for requests that reach no site
	php      map[string]phpBackend // the PHP backends, by name
	upstream map[string]*proxy.Upstream
	caches   map[string]*cache.Store // the response cache zones, by name (owned by the Server)
	sites    map[string]*siteRuntime // by site name

	mu      sync.Mutex
	refs    int  // requests using this generation
	retired bool // replaced by a newer one; closes when the last request is done
	closed  bool
	closers []io.Closer // document roots and log files
}

// acquire registers a request. It fails only for a generation whose resources
// are already closed, in which case the caller loads the current one again.
func (g *generation) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.refs++
	return true
}

// release ends a request's use of g.
func (g *generation) release() {
	g.mu.Lock()
	g.refs--
	closeNow := g.retired && g.refs == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	g.mu.Unlock()
	if closeNow {
		g.closeAll()
	}
}

// retire marks g as replaced. Its files and logs are closed once no request
// uses it any more (at once if none does).
func (g *generation) retire() {
	g.mu.Lock()
	g.retired = true
	closeNow := g.refs == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	g.mu.Unlock()
	if closeNow {
		g.closeAll()
	}
}

// closeNow closes the resources regardless of running requests (shutdown).
func (g *generation) closeNow() {
	g.mu.Lock()
	already := g.closed
	g.closed = true
	g.mu.Unlock()
	if !already {
		g.closeAll()
	}
}

func (g *generation) closeAll() {
	for _, c := range g.closers {
		c.Close()
	}
	g.closers = nil
}

// poolCloser closes a FastCGI connection pool with the generation.
type poolCloser struct{ c *fastcgi.Client }

func (p poolCloser) Close() error {
	p.c.Close()
	return nil
}

// sharedPool is a php-cgi process pool and the generations using it.
type sharedPool struct {
	pool *phpproc.Pool
	refs int
	name string // the first backend that asked for it
	spec phpproc.Spec
}

// PHPPoolInfo describes one managed php-cgi pool.
type PHPPoolInfo struct {
	Name      string
	Command   string
	Processes int // wanted
	Alive     int // running now
	Restarts  int64
}

// PHPPools lists the managed php-cgi pools in use, sorted by name.
func (s *Server) PHPPools() []PHPPoolInfo {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	out := make([]PHPPoolInfo, 0, len(s.pools))
	for _, sp := range s.pools {
		out = append(out, PHPPoolInfo{Name: sp.name, Command: sp.spec.Command, Processes: sp.spec.Processes, Alive: sp.pool.Alive(), Restarts: sp.pool.Restarts()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// managedPool returns the process pool for a backend with `command`, starting
// it if no generation uses one with the same settings yet. The closer gives the
// generation's hold on it back; the last one stops the processes.
func (s *Server) managedPool(b model.PHPBackend) (phpBackend, io.Closer, error) {
	spec := phpproc.Spec{
		Command:     b.Command,
		Processes:   b.Processes,
		MaxRequests: b.MaxRequests,
		PHPIni:      b.PHPIni,
		Timeout:     time.Duration(b.Timeout),
	}
	key := spec.Key()

	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	sp := s.pools[key]
	if sp == nil {
		pool, err := phpproc.Start(spec, s.log.With("php_backend", b.Name))
		if err != nil {
			return nil, nil, err
		}
		sp = &sharedPool{pool: pool, name: b.Name, spec: spec}
		if s.pools == nil {
			s.pools = map[string]*sharedPool{}
		}
		s.pools[key] = sp
	}
	sp.refs++
	return sp.pool, poolRelease(func() error {
		s.poolMu.Lock()
		defer s.poolMu.Unlock()
		if sp.refs--; sp.refs == 0 {
			delete(s.pools, key)
			sp.pool.Close()
		}
		return nil
	}), nil
}

type poolRelease func() error

func (f poolRelease) Close() error { return f() }
