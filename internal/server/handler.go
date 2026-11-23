package server

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/logging"
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
	cert         *tls.Certificate // set for tls manual
}

// listenerHandler routes the requests of one listener to its sites.
type listenerHandler struct {
	srv      *Server
	listener string
}

func (h *listenerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &recorder{ResponseWriter: w}

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
