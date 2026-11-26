package admin

import (
	"errors"
	"net/http"
	"os"

	"github.com/laikait/lotus/internal/sitefile"
)

// The Users page and the configuration history. Both are for the admin role only.

type userView struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Self bool   `json:"self"`
}

func (s *Server) usersEnabled(w http.ResponseWriter) bool {
	if s.opts.Users.Path == "" {
		writeError(w, http.StatusNotFound, "accounts are not available")
		return false
	}
	return true
}

func (s *Server) userList(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.usersEnabled(w) {
		return
	}
	list, err := s.opts.Users.List()
	if err != nil {
		s.log.Error("reading the users file failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the accounts file")
		return
	}
	out := make([]userView, 0, len(list))
	for _, u := range list {
		out = append(out, userView{Name: u.Name, Role: u.Role, Self: u.Name == sess.user})
	}
	writeJSON(w, http.StatusOK, out)
}

type userBody struct {
	Name            string `json:"name"`
	Role            string `json:"role"`
	Password        string `json:"password"`
	CurrentPassword string `json:"current_password"`
}

func (s *Server) userAdd(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.usersEnabled(w) {
		return
	}
	var b userBody
	if !decodeBody(w, r, &b) {
		return
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	list, err := s.opts.Users.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the accounts file")
		return
	}
	for _, u := range list {
		if u.Name == b.Name {
			writeError(w, http.StatusConflict, "an account with this name exists; change it instead")
			return
		}
	}
	if err := s.opts.Users.Set(b.Name, b.Role, b.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.log.Info("account added", "account", b.Name, "role", b.Role, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// userChange changes the role and/or the password of an account.
func (s *Server) userChange(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.usersEnabled(w) {
		return
	}
	name := r.PathValue("name")
	var b userBody
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Role == "" && b.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "give a new role or a new password")
		return
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	list, err := s.opts.Users.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the accounts file")
		return
	}
	var target *User
	for i := range list {
		if list[i].Name == name {
			target = &list[i]
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, ErrNoUser.Error())
		return
	}
	self := name == sess.user
	if b.Role != "" && b.Role != target.Role {
		if b.Role != RoleAdmin && b.Role != RoleViewer {
			writeError(w, http.StatusUnprocessableEntity, "the role must be admin or viewer")
			return
		}
		if self {
			writeError(w, http.StatusUnprocessableEntity, "you can't change your own role; ask another admin")
			return
		}
		if target.Role == RoleAdmin && Admins(list) <= 1 {
			writeError(w, http.StatusUnprocessableEntity, "this is the last admin; make another account an admin first")
			return
		}
	}
	if b.Password != "" && self {
		if _, ok := s.opts.Users.Verify(name, b.CurrentPassword); !ok {
			writeError(w, http.StatusForbidden, "the current password is wrong")
			return
		}
	}
	role := target.Role
	if b.Role != "" {
		role = b.Role
	}
	if b.Password != "" {
		err = s.opts.Users.Set(name, role, b.Password) // checks the password rules
	} else {
		err = s.opts.Users.SetRole(name, role)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Sessions keep the role they were opened with, so a changed account must sign in again. Your own session stays.
	keep := ""
	if self {
		keep = sessionIDOf(r)
	}
	n := s.revoke(name, keep)
	s.log.Info("account changed", "account", name, "role", role, "password_changed", b.Password != "", "sessions_ended", n, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_ended": n})
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.usersEnabled(w) {
		return
	}
	name := r.PathValue("name")
	if name == sess.user {
		writeError(w, http.StatusUnprocessableEntity, "you can't delete your own account")
		return
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	list, err := s.opts.Users.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the accounts file")
		return
	}
	for _, u := range list {
		if u.Name == name && u.Role == RoleAdmin && Admins(list) <= 1 {
			writeError(w, http.StatusUnprocessableEntity, "this is the last admin; it can't be deleted")
			return
		}
	}
	if err := s.opts.Users.Delete(name); err != nil {
		if errors.Is(err, ErrNoUser) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not write the accounts file")
		return
	}
	n := s.revoke(name, "")
	s.log.Info("account deleted", "account", name, "sessions_ended", n, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func sessionIDOf(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// revoke ends every session of an account except the one with id keep, and says how many it ended.
func (s *Server) revoke(user, keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, sess := range s.sessions {
		if sess.user == user && id != keep {
			delete(s.sessions, id)
			n++
		}
	}
	return n
}

// ---- history ----

func (s *Server) configHistory(w http.ResponseWriter, r *http.Request, _ *session) {
	name := r.URL.Query().Get("name")
	list, err := s.opts.Sites.History(name)
	if err != nil {
		s.configFailure(w, r, nil, "history", name, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "versions": list, "keep": sitefile.HistoryKeep})
}

func (s *Server) configVersion(w http.ResponseWriter, r *http.Request, _ *session) {
	name, id := r.URL.Query().Get("name"), r.URL.Query().Get("id")
	text, err := s.opts.Sites.ReadVersion(name, id)
	switch {
	case errors.Is(err, sitefile.ErrBadVersion), errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, sitefile.ErrBadVersion.Error())
	case err != nil:
		s.configFailure(w, r, nil, "version", name, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "id": id, "content": text})
	}
}

// keepVersion saves what a file held before a change. A failure is logged, never fatal: the change itself matters
// more than its history.
func (s *Server) keepVersion(name string, old []byte, who string) {
	if err := s.opts.Sites.Snapshot(name, old, who); err != nil {
		s.log.Warn("could not keep the earlier version", "file", name, "error", err)
	}
}
