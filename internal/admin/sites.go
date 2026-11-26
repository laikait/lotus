package admin

import (
	"errors"
	"net/http"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/sitefile"
)

// siteBody is what the add and edit forms send.
type siteBody struct {
	sitefile.Spec
	CreateRoot bool `json:"create_root"`
}

func (s *Server) listSiteFiles(w http.ResponseWriter, r *http.Request, _ *session) {
	if s.opts.Sites == nil {
		writeError(w, http.StatusNotFound, "site files are not available")
		return
	}
	list, err := s.opts.Sites.List()
	if err != nil {
		s.log.Error("listing site files failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the sites folder")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":      list,
		"default_root": sitefile.DefaultRoot("NAME"),
		"templates":    sitefile.Templates,
		"php_backends": s.opts.Sites.PHPBackendNames(),
		"hint":         s.opts.Sites.IncludeHint(),
	})
}

func (s *Server) addSite(w http.ResponseWriter, r *http.Request, sess *session) {
	var body siteBody
	if !decodeBody(w, r, &body) {
		return
	}
	sp, err := s.opts.Sites.Add(body.Spec, body.CreateRoot)
	if err != nil {
		s.siteFailure(w, r, sess, "add", sp.Name, err)
		return
	}
	s.finishSiteChange(w, r, sess, "add", sp.Name, nil)
}

func (s *Server) updateSite(w http.ResponseWriter, r *http.Request, sess *session) {
	name := r.PathValue("name")
	var body siteBody
	if !decodeBody(w, r, &body) {
		return
	}
	old := s.opts.Sites.Read(name)
	sp, err := s.opts.Sites.Update(name, body.Spec)
	if err != nil {
		s.siteFailure(w, r, sess, "update", name, err)
		return
	}
	s.finishSiteChange(w, r, sess, "update", sp.Name, old)
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request, sess *session) {
	name := r.PathValue("name")
	old := s.opts.Sites.Read(name)
	if err := s.opts.Sites.Remove(name); err != nil {
		s.siteFailure(w, r, sess, "remove", name, err)
		return
	}
	s.finishSiteChange(w, r, sess, "remove", name, old)
}

// finishSiteChange loads the new configuration. If the server refuses it (for example a port it cannot bind), the
// file is put back, so the files on disk keep matching what is running.
func (s *Server) finishSiteChange(w http.ResponseWriter, r *http.Request, sess *session, action, name string, old []byte) {
	res, rerr := s.opts.Backend.Reload("")
	if rerr != nil {
		s.opts.Sites.Restore(name, old)
		s.log.Warn("site change undone: the reload was refused", "action", action, "site", name, "user", sess.user, "ip", remote(r), "reason", rerr.Message)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"ok": false, "message": "The server did not accept the change, so it was undone: " + rerr.Message, "diagnostics": rerr.Data,
		})
		return
	}
	if len(old) > 0 {
		s.keepVersion(sitefile.SiteFileName(name), old, sess.user)
	}
	s.log.Info("site changed", "action", action, "site", name, "user", sess.user, "ip", remote(r), "generation", res.Generation)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": res.Generation})
}

func (s *Server) siteFailure(w http.ResponseWriter, r *http.Request, sess *session, action, name string, err error) {
	var invalid *sitefile.InvalidError
	var bad *sitefile.ValidationError
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusUnprocessableEntity, bad.Msg)
	case errors.As(err, &invalid):
		s.log.Warn("site change refused", "action", action, "site", name, "user", sess.user, "ip", remote(r), "reason", invalid.Error())
		msg, rest := invalid.Error(), invalid.Diagnostics
		if without, found := withoutCode(rest, diag.CodeSiteNoACME); found {
			// The most common slip, said in plain words instead of as a raw diagnostic with a file path.
			msg, rest = "Automatic TLS needs an ACME block in main.conf. Choose Off for plain HTTP, or add the block (see docs/architecture/admin.md).", without
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": msg, "diagnostics": diagnostics(rest)})
	case errors.Is(err, sitefile.ErrExists), errors.Is(err, sitefile.ErrNotEditable):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, sitefile.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		s.log.Error("site change failed", "action", action, "site", name, "error", err)
		writeError(w, http.StatusInternalServerError, "the site file could not be written (see the server log)")
	}
}

// withoutCode drops the diagnostics with the given code and says whether there were any.
func withoutCode(in []diag.Diagnostic, code string) ([]diag.Diagnostic, bool) {
	var out []diag.Diagnostic
	found := false
	for _, d := range in {
		if d.Code == code {
			found = true
			continue
		}
		out = append(out, d)
	}
	return out, found
}

// diagnostics keeps what a person needs from each problem: where it is and what it says.
func diagnostics(in []diag.Diagnostic) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, d := range in {
		if d.Severity != diag.Error {
			continue
		}
		out = append(out, map[string]string{"where": d.Pos.String(), "message": d.Message, "hint": d.Hint})
	}
	return out
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeBodyLimit(w, r, v, maxBody)
}
