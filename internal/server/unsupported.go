package server

import (
	"fmt"
	"strings"

	"github.com/laikait/lotus/internal/config/model"
)

// UnsupportedError lists configuration that is valid but not implemented by
// this version of the server yet. The server refuses to start rather than
// silently ignore it: a directive that does nothing, especially a
// security-sensitive one, is worse than an error.
type UnsupportedError struct {
	Items []string
}

func (e *UnsupportedError) Error() string {
	return "the configuration uses features this version of lotus does not support yet:\n  - " +
		strings.Join(e.Items, "\n  - ")
}

// usesPHP reports whether any route, or match block in one, runs PHP.
func usesPHP(routes []model.Route) bool {
	for i := range routes {
		r := &routes[i]
		if r.Actions.PHP != "" || usesPHP(r.Children) {
			return true
		}
		for _, m := range r.Matches {
			if m.Actions.PHP != "" {
				return true
			}
		}
	}
	return false
}

// Unsupported returns what in cfg the server can't honor yet.
func Unsupported(cfg *model.Config) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	if cfg.Control.Socket != "" || cfg.Control.ViewerGroup != "" {
		add("control (the local control channel is not implemented yet)")
	}
	for _, l := range cfg.Listeners {
		if l.HTTP3 {
			add("listener %q: http3 (HTTP/3 arrives with the modern protocols phase)", l.Name)
		}
	}
	server := cfg.Limits
	for _, s := range cfg.Sites {
		if s.TLS == model.TLSAutomatic {
			add("site %q: automatic certificates (ACME arrive with the TLS phase); use `tls manual` with a certificate", s.Name)
		}
		if usesPHP(s.Routes) || usesPHP(s.Named) {
			add("site %q: php (FastCGI arrives with the PHP phase)", s.Name)
		}
		// Connection-level limits belong to a listener, not a site, so a site
		// can't have its own values yet. Only the request body limit is per site.
		l := s.Limits
		l.MaxRequestBody = server.MaxRequestBody
		if l != server {
			add("site %q: limits other than max_request_body differ from the server's (timeouts, header size and connection limits are server-wide for now)", s.Name)
		}
	}
	return out
}
