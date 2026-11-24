package server

import (
	"errors"
	"fmt"
	"reflect"
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
	err := s.reloadLocked(cfg)
	s.countReload(err)
	return err
}

// reloadLocked is Reload for callers that hold reloadMu.
func (s *Server) reloadLocked(cfg *model.Config) error {
	start := time.Now()

	if items := Unsupported(cfg); len(items) > 0 {
		return &UnsupportedError{Items: items}
	}
	old := s.gen.Load()
	if items := append(restartRequired(old.cfg, cfg), s.restartRequiredACME(cfg)...); len(items) > 0 {
		return &RestartRequiredError{Items: items}
	}

	if s.acmeManager() == nil {
		if err := s.setupACME(cfg); err != nil {
			return fmt.Errorf("reload failed, the running configuration was kept: %w", err)
		}
	}
	g, err := s.buildGeneration(cfg)
	if err != nil {
		return fmt.Errorf("reload failed, the running configuration was kept: %w", err)
	}
	s.publish(g)
	old.retire()
	s.manageCertificates(cfg)
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
	if !reflect.DeepEqual(old.Metrics, cfg.Metrics) {
		out = append(out, "the metrics listener changed")
	}
	if old.Control != cfg.Control {
		out = append(out, "the control settings changed")
	}
	if old.TLS.MinVersion != cfg.TLS.MinVersion {
		out = append(out, "tls min_version changed")
	}
	return out
}

// restartRequiredACME is restartRequired for what depends on the running
// manager: how certificates are obtained can't change in a reload.
func (s *Server) restartRequiredACME(cfg *model.Config) []string { return s.acmeRestartRequired(cfg) }

// ErrReloadInProgress is returned by TryReload when another reload is running.
var ErrReloadInProgress = errors.New("a reload is already in progress")

// TryReload is Reload that fails at once, instead of waiting, when another
// reload is running.
func (s *Server) TryReload(cfg *model.Config) error {
	if !s.reloadMu.TryLock() {
		return ErrReloadInProgress
	}
	defer s.reloadMu.Unlock()
	err := s.reloadLocked(cfg)
	s.countReload(err)
	return err
}

// ReopenLogs reopens every log file, which is what log rotation needs after a
// file was moved. It is a reload of the running configuration. It returns how
// many log files were opened.
func (s *Server) ReopenLogs() (int, error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	old := s.gen.Load()
	g, err := s.buildGeneration(old.cfg)
	if err != nil {
		return 0, fmt.Errorf("reopening logs failed, the running configuration was kept: %w", err)
	}
	s.publish(g)
	old.retire()
	s.log.Info("log files reopened", "files", g.logFiles)
	return g.logFiles, nil
}

// Generation numbers the configuration in use: 1 at start, plus one for each
// reload or log reopen.
func (s *Server) Generation() uint64 { return s.gen.Load().number }

// Config returns the configuration in use. It must not be modified.
func (s *Server) Config() *model.Config { return s.gen.Load().cfg }
