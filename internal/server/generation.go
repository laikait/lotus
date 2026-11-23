package server

import (
	"io"
	"sync"

	"github.com/laikait/lotus/internal/clientip"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/logging"
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
	fallback *logging.Access // access log for requests that reach no site

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
