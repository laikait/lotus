package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/sitefile"
)

// The configuration editor. The raw files can hold secrets (tokens, keys' paths), so every route here needs the
// admin role, even for reading: a viewer sees the redacted configuration elsewhere, never the files.

type fileBody struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (s *Server) configFiles(w http.ResponseWriter, r *http.Request, _ *session) {
	files, err := s.opts.Sites.Files()
	if err != nil {
		s.log.Error("listing configuration files failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the configuration folder")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "max_size": sitefile.MaxFileSize})
}

func (s *Server) configRead(w http.ResponseWriter, r *http.Request, _ *session) {
	name := r.URL.Query().Get("name")
	text, err := s.opts.Sites.ReadFile(name)
	if err != nil {
		s.configFailure(w, r, nil, "read", name, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "content": text})
}

func (s *Server) configValidate(w http.ResponseWriter, r *http.Request, sess *session) {
	var b fileBody
	if !decodeBodyLimit(w, r, &b, sitefile.MaxFileSize+4096) {
		return
	}
	diags, err := s.opts.Sites.CheckFile(b.Name, b.Content)
	if err != nil {
		s.configFailure(w, r, sess, "validate", b.Name, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "diagnostics": diagnosticsAll(diags)})
}

func (s *Server) configSave(w http.ResponseWriter, r *http.Request, sess *session) {
	var b fileBody
	if !decodeBodyLimit(w, r, &b, sitefile.MaxFileSize+4096) {
		return
	}
	old, err := s.opts.Sites.SaveFile(b.Name, b.Content)
	if err != nil {
		s.configFailure(w, r, sess, "save", b.Name, err)
		return
	}
	res, rerr := s.opts.Backend.Reload("")
	if rerr != nil {
		s.opts.Sites.RestoreFile(b.Name, old)
		s.log.Warn("configuration edit undone: the reload was refused", "file", b.Name, "user", sess.user, "ip", remote(r), "reason", rerr.Message)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"ok": false, "message": "The server did not accept the change, so it was undone: " + rerr.Message, "diagnostics": rerr.Data,
		})
		return
	}
	s.keepVersion(b.Name, old, sess.user)
	s.log.Info("configuration file saved", "file", b.Name, "user", sess.user, "ip", remote(r), "generation", res.Generation)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": res.Generation})
}

func (s *Server) configFailure(w http.ResponseWriter, r *http.Request, sess *session, action, name string, err error) {
	var invalid *sitefile.InvalidError
	switch {
	case errors.As(err, &invalid):
		who := "-"
		if sess != nil {
			who = sess.user
		}
		s.log.Warn("configuration edit refused", "action", action, "file", name, "user", who, "ip", remote(r), "reason", invalid.Error())
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": invalid.Error(), "diagnostics": diagnostics(invalid.Diagnostics)})
	case errors.Is(err, sitefile.ErrBadName):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, sitefile.ErrTooBig), errors.Is(err, sitefile.ErrBadContent):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "there is no such file")
	default:
		s.log.Error("configuration editor failed", "action", action, "file", name, "error", err)
		writeError(w, http.StatusInternalServerError, "the file could not be read or written (see the server log)")
	}
}

// diagnosticsAll is like diagnostics but keeps warnings too, for a valid file that has some.
func diagnosticsAll(in []diag.Diagnostic) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, d := range in {
		if d.Severity == diag.Note {
			continue
		}
		out = append(out, map[string]string{"severity": d.Severity.String(), "where": d.Pos.String(), "message": d.Message, "hint": d.Hint})
	}
	return out
}

func decodeBodyLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "the request is not valid JSON for this form")
		return false
	}
	return true
}
