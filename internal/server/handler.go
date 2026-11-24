package server

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fspath"
	"github.com/laikait/lotus/internal/logging"
	"github.com/laikait/lotus/internal/proxy"
	"github.com/laikait/lotus/internal/staticfiles"
	"github.com/laikait/lotus/internal/vhost"
)

// serverHeader is sent on every response. It names the product but not the
// version, so it doesn't help an attacker pick exploits.
const serverHeader = "lotus"

// siteRuntime is everything needed to serve one site.
type siteRuntime struct {
	site         *model.Site
	defaultFiles *staticfiles.Handler            // nil when the site has no document root
	handlers     map[string]*staticfiles.Handler // by effective root and index, for routes
	access       *logging.Access
	log          *slog.Logger
	headers      []model.Header
	maxBody      int64
	cert         *tls.Certificate           // set for tls manual
	php          map[string]phpBackend      // PHP backends of this configuration, by name
	phpSite      bool                       // some route of the site runs PHP
	tlsAuto      bool                       // the site gets its certificate from ACME
	upstreams    map[string]*proxy.Upstream // reverse proxy upstreams of this configuration, by name

	// .htaccess support; ht is nil when the site doesn't read .htaccess files.
	rate          *rateLimit                  // limit on all requests of the site
	routeRates    map[*model.Route]*rateLimit // limits of single routes
	ht            *htRuntime
	resolvers     map[string]*fspath.Resolver // by document root
	extraHandlers sync.Map                    // static handlers with an index list a .htaccess chose
	protected     map[string]bool             // names of the site's security_headers
}

// listenerHandler routes the requests of one listener to its sites.
type listenerHandler struct {
	srv      *Server
	listener string
	bl       *boundListener
}

func (h *listenerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	served(r.Context())
	rec := &recorder{ResponseWriter: w}
	// A certificate authority validating a name by HTTP-01. It is answered
	// before anything else (no site, limit or log line involved).
	if h.srv.handleACMEChallenge(w, r) {
		return
	}

	// The whole request uses the configuration that is current now, even if a
	// reload happens while it runs. The generation is released at the end.
	gen := h.srv.acquire()
	if gen == nil {
		http.Error(rec, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	defer gen.release()
	table := gen.tables[h.listener]
	access := gen.fallback

	// The client address is the connection's peer, or, when that peer is a
	// trusted proxy, the address it reports in Forwarded / X-Forwarded-For.
	// Access logs, match conditions and {remote_ip} all use this one value.
	client, _ := netip.ParseAddr(remoteIP(r))
	client = gen.clients.Resolve(client, r.Header)
	clientStr := addrString(client, r)
	defer func() {
		access.Log(logging.Entry{
			Time:      start,
			RemoteIP:  clientStr,
			Method:    r.Method,
			URI:       r.RequestURI,
			Proto:     r.Proto,
			Status:    rec.statusCode(),
			Bytes:     rec.bytes,
			Referer:   r.Referer(),
			UserAgent: r.UserAgent(),
		})
	}()
	rec.Header().Set("Server", serverHeader)
	if r.ProtoMajor == 3 {
		h3Deadlines(w, gen.cfg.Limits)
	} else if alt := h.bl.altSvc.Load(); alt != nil && r.TLS != nil {
		rec.Header().Set("Alt-Svc", *alt) // HTTP/3 is available on the same port number
	}
	// A chunked request body is framed by Transfer-Encoding. If the request also
	// had a Content-Length (which net/http removes before we see it), a proxy in
	// front may have used that one instead and so disagrees with us about where
	// the request ends: classic request smuggling. RFC 9112 section 6.1 says the
	// connection must then be closed after the response, so whatever follows is
	// never read as a request. net/http offers no way to see the conflict, so
	// the connection is closed after every chunked HTTP/1.x request.
	if r.ProtoMajor == 1 && len(r.TransferEncoding) > 0 {
		rec.Header().Set("Connection", "close")
	}

	host, ok := vhost.NormalizeHost(r.Host)
	if !ok {
		http.Error(rec, "bad request: invalid Host header", http.StatusBadRequest)
		return
	}
	site, _, ok := table.Lookup(host)
	if !ok {
		http.Error(rec, "no site is configured for this listener", http.StatusNotFound)
		return
	}
	access = site.access

	// With TLS, the Host header must select the same site as the name the
	// client asked for in the handshake. Otherwise a connection opened (or,
	// with HTTP/2, reused) for one site could be used to reach another.
	if r.TLS != nil && r.TLS.ServerName != "" {
		if sni, ok := vhost.NormalizeHost(r.TLS.ServerName); ok {
			if bySNI, _, found := table.Lookup(sni); found && bySNI != site {
				http.Error(rec, "misdirected request", http.StatusMisdirectedRequest)
				return
			}
		}
	}

	for _, hd := range site.headers {
		rec.Header().Set(hd.Name, hd.Value)
	}
	site.serve(rec, r, client)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
