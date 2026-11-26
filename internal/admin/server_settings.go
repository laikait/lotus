package admin

import (
	"net/http"
	"strings"

	"github.com/laikait/lotus/internal/sitefile"
)

// PHP pools and the HTTPS wizard. Both write the managed server file (sites.d/00-lotus-server.conf) through
// sitefile, so they get the same checks as the Sites page: the whole configuration is validated, the file is put back
// if it is not valid or the server refuses it.

// restartPhrase is in the server's message when a change can't be applied by a reload (see server.RestartRequiredError).
const restartPhrase = "need a restart"

func (s *Server) serverSpec(w http.ResponseWriter) (sitefile.ServerSpec, bool) {
	sp, editable, err := s.opts.Sites.Server()
	if err != nil {
		s.log.Error("reading the server settings file failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the server settings file")
		return sp, false
	}
	if !editable {
		writeError(w, http.StatusConflict, sitefile.ErrServerFileByHand.Error())
		return sp, false
	}
	return sp, true
}

// ---- PHP pools ----

func (s *Server) phpPoolList(w http.ResponseWriter, r *http.Request, _ *session) {
	sp, editable, err := s.opts.Sites.Server()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the server settings file")
		return
	}
	pools := sp.PHP
	if pools == nil {
		pools = []sitefile.PHPPoolSpec{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pools": pools, "editable": editable, "can_restart": s.opts.PHPRestart != nil})
}

func (s *Server) phpPoolSave(w http.ResponseWriter, r *http.Request, sess *session) {
	name := r.PathValue("name")
	var body sitefile.PHPPoolSpec
	if !decodeBody(w, r, &body) {
		return
	}
	body.Name = name
	sp, ok := s.serverSpec(w)
	if !ok {
		return
	}
	replaced := false
	for i := range sp.PHP {
		if sp.PHP[i].Name == name {
			sp.PHP[i], replaced = body, true
		}
	}
	if !replaced {
		sp.PHP = append(sp.PHP, body)
	}
	s.saveServer(w, r, sess, "php pool saved", name, sp, false)
}

func (s *Server) phpPoolDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	name := r.PathValue("name")
	sp, ok := s.serverSpec(w)
	if !ok {
		return
	}
	var keep []sitefile.PHPPoolSpec
	found := false
	for _, p := range sp.PHP {
		if p.Name == name {
			found = true
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		writeError(w, http.StatusNotFound, "there is no such pool in the server settings file (pools in main.conf are changed in the configuration editor)")
		return
	}
	sp.PHP = keep
	s.saveServer(w, r, sess, "php pool deleted", name, sp, false)
}

func (s *Server) phpPoolRestart(w http.ResponseWriter, r *http.Request, sess *session) {
	name := r.PathValue("name")
	if s.opts.PHPRestart == nil || !s.opts.PHPRestart(name) {
		writeError(w, http.StatusNotFound, "no php-cgi pool of this name is running")
		return
	}
	s.log.Info("php pool restarted", "pool", name, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- HTTPS wizard ----

func (s *Server) httpsStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	sp, editable, err := s.opts.Sites.Server()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the server settings file")
		return
	}
	listeners, email, hasACME, valid := s.opts.Sites.Running()
	type listener struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		HTTPS   bool   `json:"https"`
	}
	ls := []listener{}
	for _, l := range listeners {
		ls = append(ls, listener{l.Name, l.Address, l.HTTPS})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spec": sp.HTTPS, "editable": editable, "listeners": ls, "acme": hasACME, "acme_email": email, "files_valid": valid,
		"staging_ca": sitefile.StagingCA,
	})
}

func (s *Server) httpsSave(w http.ResponseWriter, r *http.Request, sess *session) {
	var body sitefile.HTTPSSpec
	if !decodeBody(w, r, &body) {
		return
	}
	sp, ok := s.serverSpec(w)
	if !ok {
		return
	}
	sp.HTTPS = &body
	s.saveServer(w, r, sess, "https saved", "https", sp, true)
}

func (s *Server) httpsDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	sp, ok := s.serverSpec(w)
	if !ok {
		return
	}
	if sp.HTTPS == nil {
		writeError(w, http.StatusNotFound, "the wizard has not set up HTTPS")
		return
	}
	sp.HTTPS = nil
	s.saveServer(w, r, sess, "https removed", "https", sp, true)
}

// saveServer writes the server settings file, loads it, and answers. A reload the server refuses undoes the change,
// except for settings that need a restart (listeners, TLS) when restartOK: those stay in the file, and the answer
// says to restart the service.
func (s *Server) saveServer(w http.ResponseWriter, r *http.Request, sess *session, action, what string, sp sitefile.ServerSpec, restartOK bool) {
	old, err := s.opts.Sites.SaveServer(sp)
	if err != nil {
		s.siteFailure(w, r, sess, action, what, err)
		return
	}
	res, rerr := s.opts.Backend.Reload("")
	if rerr != nil {
		if restartOK && strings.Contains(rerr.Message, restartPhrase) {
			s.keepVersion(sitefile.SiteFileName(sitefile.ServerFile), old, sess.user)
			s.log.Info("server settings saved; a restart is needed", "action", action, "user", sess.user, "ip", remote(r))
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": true, "message": rerr.Message})
			return
		}
		s.opts.Sites.Restore(sitefile.ServerFile, old)
		s.log.Warn("server settings change undone: the reload was refused", "action", action, "user", sess.user, "ip", remote(r), "reason", rerr.Message)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"ok": false, "message": "The server did not accept the change, so it was undone: " + rerr.Message, "diagnostics": rerr.Data,
		})
		return
	}
	s.keepVersion(sitefile.SiteFileName(sitefile.ServerFile), old, sess.user)
	s.log.Info("server settings changed", "action", action, "what", what, "user", sess.user, "ip", remote(r), "generation", res.Generation)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": res.Generation})
}
