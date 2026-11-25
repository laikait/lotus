package admin

import (
	"net/http"

	"github.com/laikait/lotus/internal/control"
)

// PHPPool describes one managed php-cgi pool, for the PHP page.
type PHPPool struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Processes int    `json:"processes"` // wanted
	Alive     int    `json:"alive"`     // running now
	Restarts  int64  `json:"restarts"`
}

// httpStatusOf maps a control error to an HTTP status.
func httpStatusOf(e *control.Error) int {
	switch e.Code {
	case control.CodeInvalidParams:
		return http.StatusUnprocessableEntity
	case control.CodeNotFound:
		return http.StatusNotFound
	case control.CodeReloadInProgress:
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func (s *Server) cacheStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	zones, err := s.opts.Backend.CacheStatus("")
	if err != nil {
		writeError(w, httpStatusOf(err), err.Message)
		return
	}
	if zones == nil {
		zones = []control.CacheZone{}
	}
	writeJSON(w, http.StatusOK, zones)
}

type purgeBody struct {
	Zone   string `json:"zone"`
	Host   string `json:"host"`
	Path   string `json:"path"`
	Prefix string `json:"prefix"`
	All    bool   `json:"all"`
}

func (s *Server) cachePurge(w http.ResponseWriter, r *http.Request, sess *session) {
	var b purgeBody
	if !decodeBody(w, r, &b) {
		return
	}
	n, err := s.opts.Backend.CachePurge(control.CachePurgeParams{Zone: b.Zone, Host: b.Host, Path: b.Path, Prefix: b.Prefix, All: b.All})
	if err != nil {
		writeError(w, httpStatusOf(err), err.Message)
		return
	}
	s.log.Info("cache purged", "zone", b.Zone, "host", b.Host, "path", b.Path, "prefix", b.Prefix, "all", b.All, "removed", n, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "purged": n})
}

func (s *Server) reopenLogs(w http.ResponseWriter, r *http.Request, sess *session) {
	n, err := s.opts.Backend.ReopenLogs()
	if err != nil {
		writeError(w, httpStatusOf(err), err.Message)
		return
	}
	s.log.Info("logs reopened", "files", n, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reopened": n})
}

func (s *Server) phpStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	pools := []PHPPool{}
	if s.opts.PHP != nil {
		pools = s.opts.PHP()
		if pools == nil {
			pools = []PHPPool{}
		}
	}
	writeJSON(w, http.StatusOK, pools)
}
