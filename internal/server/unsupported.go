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

// Unsupported returns what in cfg the server can't honor yet.
func Unsupported(cfg *model.Config) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	if cfg.Control.ViewerGroup != "" {
		add("control viewer_group (read-only roles need the peer's identity, which is not wired up yet; every client that can open the socket is an operator)")
	}
	for _, l := range cfg.Listeners {
		if l.HTTP3 {
			add("listener %q: http3 (HTTP/3 arrives with the modern protocols phase)", l.Name)
		}
	}
	server := cfg.Limits
	for _, s := range cfg.Sites {
		if s.TLS == model.TLSAutomatic && cfg.TLS.ACME == nil {
			add("site %q: automatic certificates need an acme block in tls", s.Name)
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
