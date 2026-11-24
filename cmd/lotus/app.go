package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/control"
	"github.com/laikait/lotus/internal/server"
	"github.com/laikait/lotus/pkg/version"
)

// stopRequest is a request to shut down, from a signal or the control channel.
type stopRequest struct {
	graceful bool
	timeout  time.Duration
}

// app is a running server plus what is needed to reload and control it. It
// implements control.Backend.
type app struct {
	path     string // the configuration file; a successful reload from another file replaces it
	insecure bool
	srv      *server.Server
	rt       *runtimeSettings
	log      *slog.Logger
	stderr   interface{ Write([]byte) (int, error) }
	started  time.Time

	reloadMu sync.Mutex // held for a whole reload: read, check, switch
	mu       sync.Mutex // guards path and res
	res      *config.Result

	stop chan stopRequest
}

var _ control.Backend = (*app)(nil)

// reload re-reads the configuration (from path, or the current file when
// path is empty) and switches to it. On any failure the running configuration
// stays. The error is a control error so the control channel can return it as
// it is; callers that only log it use its message.
func (a *app) reload(path string) (control.ReloadResult, *control.Error) {
	if !a.reloadMu.TryLock() {
		return control.ReloadResult{}, control.NewError(control.CodeReloadInProgress, "a reload is already in progress", nil)
	}
	defer a.reloadMu.Unlock()

	a.mu.Lock()
	current := a.path
	a.mu.Unlock()
	if path == "" {
		path = current
	} else if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	a.log.Info("reloading configuration", "config", path)
	res, diags := config.Check(path, loader.Options{InsecureIncludes: a.insecure})
	for _, d := range diags {
		printDiagnostic(a.stderr, d)
	}
	if res == nil {
		a.log.Error("reload refused: the configuration is invalid; the running configuration was kept", "config", path)
		return control.ReloadResult{}, control.NewError(control.CodeConfigInvalid,
			"the configuration is invalid; the running configuration was kept",
			map[string]any{"diagnostics": diags})
	}
	if err := a.srv.Reload(res.Config); err != nil {
		a.log.Error("reload failed", "error", err)
		return control.ReloadResult{}, control.NewError(control.CodeConfigInvalid, err.Error(), nil)
	}
	a.rt.apply(res.Config.Runtime.MaxProcs, res.Config.Runtime.MemoryLimit)
	a.mu.Lock()
	a.path, a.res = path, res
	a.mu.Unlock()

	out := control.ReloadResult{Generation: a.srv.Generation(), Diagnostics: []json.RawMessage{}}
	for _, d := range diags {
		if b, err := json.Marshal(d); err == nil {
			out.Diagnostics = append(out.Diagnostics, b)
		}
	}
	return out, nil
}

// Reload implements control.Backend.
func (a *app) Reload(path string) (control.ReloadResult, *control.Error) { return a.reload(path) }

// Status implements control.Backend.
func (a *app) Status() control.Status {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	a.mu.Lock()
	path := a.path
	a.mu.Unlock()
	cfg := a.srv.Config()
	st := control.Status{
		Version:     version.Version,
		PID:         os.Getpid(),
		Started:     a.started,
		Uptime:      time.Since(a.started).Round(time.Second).String(),
		Generation:  a.srv.Generation(),
		ConfigFile:  path,
		Sites:       len(cfg.Sites),
		Goroutines:  runtime.NumGoroutine(),
		MemoryBytes: m.Alloc,
		Rejected:    a.srv.RejectedConnections(),
		Dropped:     a.srv.DroppedRequests(),
		Listeners:   []control.Listener{},
	}
	addrs := a.srv.Addrs()
	for _, l := range cfg.Listeners {
		addr := l.Address
		if bound, ok := addrs[l.Name]; ok {
			addr = bound.String()
		}
		st.Listeners = append(st.Listeners, control.Listener{Name: l.Name, Address: addr, HTTPS: l.HTTPS})
	}
	return st
}

// Health implements control.Backend.
func (a *app) Health() control.Health {
	h := control.Health{Status: "ok", Checks: []control.HealthCheck{}}
	cfg := a.srv.Config()
	bound := len(a.srv.Addrs())
	check := control.HealthCheck{Name: "listeners", Status: "ok", Detail: fmt.Sprintf("%d of %d listening", bound, len(cfg.Listeners))}
	if bound != len(cfg.Listeners) {
		check.Status, h.Status = "failed", "degraded"
	}
	h.Checks = append(h.Checks, check)
	return h
}

// Sites implements control.Backend.
func (a *app) Sites() []control.Site {
	cfg := a.srv.Config()
	out := make([]control.Site, 0, len(cfg.Sites))
	for _, s := range cfg.Sites {
		aliases, listeners := s.Aliases, s.Listeners
		if aliases == nil {
			aliases = []string{}
		}
		if listeners == nil {
			listeners = []string{}
		}
		out = append(out, control.Site{Name: s.Name, Aliases: aliases, Listeners: listeners, TLS: s.TLS})
	}
	return out
}

// Certificates implements control.Backend.
func (a *app) Certificates() []control.Certificate {
	return toControlCerts(a.srv.Certificates(), time.Now())
}

// toControlCerts describes the server's certificates for the control channel.
func toControlCerts(infos []server.CertInfo, now time.Time) []control.Certificate {
	out := make([]control.Certificate, 0, len(infos))
	for _, c := range infos {
		domains := c.Domains
		if domains == nil {
			domains = []string{c.Name}
		}
		cert := control.Certificate{
			Name: c.Name, Domains: domains, Issuer: c.Issuer, Managed: c.Source == "acme", Source: c.Source, Site: c.Site,
			Status: control.CertStatus(c.Ready, c.NotAfter, now, control.CertWarnDays*24*time.Hour),
		}
		if c.Ready {
			na := c.NotAfter
			cert.NotAfter = &na
		}
		out = append(out, cert)
	}
	return out
}

// Stop implements control.Backend.
func (a *app) Stop(graceful bool, timeout time.Duration) {
	select {
	case a.stop <- stopRequest{graceful: graceful, timeout: timeout}:
	default: // a stop is already requested
	}
}

// ReopenLogs implements control.Backend.
func (a *app) ReopenLogs() (int, *control.Error) {
	n, err := a.srv.ReopenLogs()
	if err != nil {
		return 0, control.NewError(control.CodeInternal, err.Error(), nil)
	}
	return n, nil
}

// InspectConfig implements control.Backend.
func (a *app) InspectConfig(site string) (json.RawMessage, *control.Error) {
	a.mu.Lock()
	res := a.res
	a.mu.Unlock()
	out, err := res.InspectJSON(site)
	if errors.Is(err, config.ErrNoSuchSite) {
		return nil, control.NewError(control.CodeNotFound, fmt.Sprintf("there is no site named %q", site), nil)
	}
	if err != nil {
		return nil, control.NewError(control.CodeInternal, "could not render the configuration", nil)
	}
	return out, nil
}

// diagnosticsOf extracts the diagnostics from a configuration-invalid error.
func diagnosticsOf(e *control.Error) []diag.Diagnostic {
	var data struct {
		Diagnostics []diag.Diagnostic `json:"diagnostics"`
	}
	if len(e.Data) > 0 {
		_ = json.Unmarshal(e.Data, &data)
	}
	return data.Diagnostics
}

// openControl starts the control channel. With no `control { socket }` in the
// configuration it uses the platform default, and a failure there only
// disables the channel (with a warning): a development server shouldn't need
// /run/lotus. A path the administrator chose that doesn't work is an error.
// The returned closer is nil when the channel is off.
func (a *app) openControl(cfg *model.Config) (io.Closer, error) {
	path, explicit := cfg.Control.Socket, cfg.Control.Socket != ""
	if !explicit {
		path = control.DefaultSocketPath()
	}
	ln, err := control.Listen(path)
	if err != nil {
		if explicit {
			return nil, err
		}
		a.log.Warn("control channel disabled: lotus reload, stop and status will not work", "socket", path, "reason", err.Error(),
			"fix", "set control { socket \"/path/to/control.sock\" } in the configuration")
		return nil, nil
	}
	cs := control.NewServer(control.Options{
		Backend: a, ServerName: "lotus/" + version.Version, PID: os.Getpid(), Logger: a.log,
	})
	go cs.Serve(ln)
	a.log.Info("control channel ready", "socket", path)
	return cs, nil
}
