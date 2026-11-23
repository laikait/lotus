package server

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
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
	table    *vhost.Table[*siteRuntime]
	fallback *logging.Access // access log for requests that reach no site
}

func (h *listenerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &recorder{ResponseWriter: w}
	access := h.fallback
	defer func() {
		access.Log(logging.Entry{
			Time:      start,
			RemoteIP:  remoteIP(r),
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
	site, _, ok := h.table.Lookup(host)
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
			if bySNI, _, found := h.table.Lookup(sni); found && bySNI != site {
				http.Error(rec, "misdirected request", http.StatusMisdirectedRequest)
				return
			}
		}
	}

	for _, hd := range site.headers {
		rec.Header().Set(hd.Name, hd.Value)
	}
	site.serve(rec, r)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
