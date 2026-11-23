package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

// RestartRequiredError means the new configuration changes something that is
// fixed while the server runs (listeners, connection limits, timeouts, the
// minimum TLS version). The running configuration stays in place.
type RestartRequiredError struct {
	Items []string
}

func (e *RestartRequiredError) Error() string {
	return "the new configuration changes settings that need a restart; the running configuration was kept:\n  - " +
		strings.Join(e.Items, "\n  - ")
}

// Reload switches to a new configuration without dropping connections.
//
// The new configuration is built completely first: document roots opened, log
// files opened, certificates loaded. Only if that succeeds is it swapped in,
// with one atomic store, so a request sees either the old configuration or the
// new one, never a mix. If anything fails, the old configuration keeps running
// untouched and the error says why. Reopening the log files (after log
// rotation) is the same operation: a reload with an unchanged configuration.
//
// What can change: sites, aliases, routes, certificates (tls manual), log
// files, security headers, per-site body limits, trusted_proxies. What can't
// (and gives a RestartRequiredError): listeners, server-wide limits and
// timeouts, the minimum TLS version.
func (s *Server) Reload(cfg *model.Config) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	start := time.Now()

	if items := Unsupported(cfg); len(items) > 0 {
		return &UnsupportedError{Items: items}
	}
	old := s.gen.Load()
	if items := restartRequired(old.cfg, cfg); len(items) > 0 {
		return &RestartRequiredError{Items: items}
	}

	g, err := s.buildGeneration(cfg)
	if err != nil {
		return fmt.Errorf("reload failed, the running configuration was kept: %w", err)
	}
	s.gen.Store(g)
	old.retire()
	s.log.Info("configuration reloaded", "sites", len(cfg.Sites), "took", time.Since(start).Round(time.Millisecond).String())
	return nil
}

// restartRequired lists the differences between two configurations that a
// running server can't apply.
func restartRequired(old, cfg *model.Config) []string {
	var out []string
	if len(old.Listeners) != len(cfg.Listeners) {
		out = append(out, fmt.Sprintf("the number of listeners changed (%d to %d)", len(old.Listeners), len(cfg.Listeners)))
	} else {
		for i := range old.Listeners {
			if old.Listeners[i] != cfg.Listeners[i] {
				out = append(out, fmt.Sprintf("listener %q changed (name, address, https or http3)", old.Listeners[i].Name))
			}
		}
	}
	// max_request_body is per site and applied at request time; the rest is fixed.
	a, b := old.Limits, cfg.Limits
	a.MaxRequestBody, b.MaxRequestBody = 0, 0
	if a != b {
		out = append(out, "server-wide limits changed (max_connections, max_header_bytes or a timeout)")
	}
	if old.TLS.MinVersion != cfg.TLS.MinVersion {
		out = append(out, "tls min_version changed")
	}
	return out
}
