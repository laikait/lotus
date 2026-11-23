package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fspath"
	"github.com/laikait/lotus/internal/htaccess"
	"github.com/laikait/lotus/internal/staticfiles"
)

// .htaccess support (docs/architecture/htaccess.md).
//
// For a request, the server finds the .htaccess files from the document root
// down to the requested directory (never above the root, never outside it),
// compiles each once and remembers it, and asks the htaccess package what they
// say: access refused, a redirect, a rewrite (which routes the request again),
// and for the response, error documents, index files and headers.

// htRuntime is a site's .htaccess state.
type htRuntime struct {
	cfg     *model.HTAccess
	allow   map[string]bool
	log     *slog.Logger
	recheck time.Duration

	mu     sync.Mutex
	dirs   map[string]*htDir // by document root and directory URL
	logged map[string]bool   // problems already reported, so a busy site doesn't flood its log
}

// htDir is what is known about one directory.
type htDir struct {
	checked time.Time
	isDir   bool
	file    *htaccess.Compiled // nil: no .htaccess
	mtime   time.Time
	size    int64
}

const maxHTCache = 4096

func newHTRuntime(cfg *model.HTAccess, log *slog.Logger) *htRuntime {
	h := &htRuntime{
		cfg: cfg, log: log, recheck: time.Duration(cfg.Recheck),
		allow: map[string]bool{}, dirs: map[string]*htDir{}, logged: map[string]bool{},
	}
	for _, c := range cfg.Allow {
		h.allow[c] = true
	}
	return h
}

// dir returns what is known about a directory, looking at the file system
// again if the last look is older than the recheck interval.
func (h *htRuntime) dir(res *fspath.Resolver, root, dirURL string) *htDir {
	key := root + "\x00" + dirURL
	now := time.Now()
	h.mu.Lock()
	d := h.dirs[key]
	h.mu.Unlock()
	if d != nil && now.Sub(d.checked) < h.recheck {
		return d
	}

	nd := &htDir{checked: now, isDir: true}
	if dirURL != "/" {
		f, err := res.Open(strings.TrimSuffix(dirURL, "/"))
		if err != nil || !f.Info.IsDir() {
			if f != nil {
				f.Close()
			}
			nd.isDir = false
		} else {
			f.Close()
		}
	}
	if nd.isDir {
		h.load(res, dirURL, d, nd)
	}
	h.mu.Lock()
	if len(h.dirs) >= maxHTCache {
		h.dirs = map[string]*htDir{}
	}
	h.dirs[key] = nd
	h.mu.Unlock()
	return nd
}

// load reads the directory's .htaccess into nd, reusing the compiled form if
// the file didn't change (same modification time and size).
func (h *htRuntime) load(res *fspath.Resolver, dirURL string, old, nd *htDir) {
	f, err := res.OpenHtaccess(dirURL)
	if err != nil {
		return // none, or not allowed to be read: treated as absent
	}
	defer f.Close()
	nd.mtime, nd.size = f.Info.ModTime(), f.Info.Size()
	if old != nil && old.file != nil && old.mtime.Equal(nd.mtime) && old.size == nd.size {
		nd.file = old.file
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, htaccess.MaxFileSize+1))
	if err != nil {
		h.log.Error("htaccess: reading the file failed", "directory", dirURL, "error", err)
		return
	}
	nodes, problems := htaccess.Parse(data)
	nd.file = htaccess.Compile(nodes, problems, htaccess.Options{Allow: h.allow})
	for _, n := range nd.file.Notes {
		h.log.Info("htaccess: "+n.Msg, "directory", dirURL, "line", n.Line)
	}
}

// layers returns the compiled files from the root down to the directory of urlPath.
func (h *htRuntime) layers(res *fspath.Resolver, root, urlPath string) []htaccess.Layer {
	var out []htaccess.Layer
	segs := strings.Split(strings.Trim(urlPath, "/"), "/")
	if urlPath == "/" || urlPath == "" {
		segs = nil
	}
	dirURL := "/"
	for i := 0; ; i++ {
		d := h.dir(res, root, dirURL)
		if !d.isDir {
			break
		}
		if d.file != nil {
			out = append(out, htaccess.Layer{DirURL: dirURL, File: d.file})
		}
		if i >= len(segs) {
			break
		}
		dirURL += segs[i] + "/"
	}
	return out
}

// problems decides whether the directives of the layers that can't be honoured
// make the request fail. In "ignore" mode only the non-sensitive ones are
// skipped: a directive that grants or restricts access is never guessed at.
func (h *htRuntime) problems(layers []htaccess.Layer) (fail string) {
	for _, l := range layers {
		for _, p := range l.File.Problems() {
			key := fmt.Sprintf("%s|%p|%d", l.DirURL, l.File, p.Line)
			skip := h.cfg.OnUnsupported == model.HTAccessIgnore && !p.Sensitive
			h.mu.Lock()
			seen := h.logged[key]
			if !seen {
				if len(h.logged) > maxHTCache {
					h.logged = map[string]bool{}
				}
				h.logged[key] = true
			}
			h.mu.Unlock()
			if !seen {
				level := slog.LevelError
				if skip {
					level = slog.LevelWarn
				}
				action := "the request fails (htaccess_unsupported error)"
				if skip {
					action = "the directive is skipped (htaccess_unsupported ignore)"
				} else if p.Sensitive && h.cfg.OnUnsupported == model.HTAccessIgnore {
					action = "the request fails: directives that grant or restrict access are never skipped"
				}
				h.log.Log(context.Background(), level, "htaccess: "+p.Msg, "directory", l.DirURL, "line", p.Line, "directive", p.Directive, "status", p.Status.String(), "action", action)
			}
			if !skip && fail == "" {
				fail = fmt.Sprintf("%s line %d: %s", l.DirURL+".htaccess", p.Line, p.Msg)
			}
		}
	}
	return fail
}

// htResult is what .htaccess says about one routing step.
type htResult struct {
	fail string // not empty: answer 500
	dec  *htaccess.Decision
}

// applyHTAccess consults the .htaccess files for the request at its current
// target. noRewrite is set after a rule with the END flag.
func (rt *siteRuntime) applyHTAccess(r *http.Request, root string, cur target, client netip.Addr, noRewrite bool) htResult {
	res := rt.resolvers[root]
	if res == nil {
		return htResult{}
	}
	// .htaccess, .htpasswd and the like are refused outright, as Apache's
	// default configuration does. Without this a catch-all rewrite rule would
	// take the (hidden, so "missing") file for a missing page and run the
	// application for it.
	if base := cur.path[strings.LastIndexByte(cur.path, '/')+1:]; len(base) >= 3 && strings.EqualFold(base[:3], ".ht") {
		return htResult{dec: &htaccess.Decision{Status: http.StatusForbidden}}
	}
	layers := rt.ht.layers(res, root, cur.path)
	if len(layers) == 0 {
		return htResult{}
	}
	if fail := rt.ht.problems(layers); fail != "" {
		return htResult{fail: fail}
	}
	req := &htaccess.Request{
		Method: r.Method, Host: r.Host, Proto: r.Proto, Remote: client,
		RequestLine: r.Method + " " + r.RequestURI + " " + r.Proto,
		Path:        cur.path, Query: cur.query,
		Header: func(n string) string { return r.Header.Get(n) },
		Root:   filepath.ToSlash(root),
		Stat: func(p string) (htaccess.FileInfo, bool) {
			f, err := res.Open(p)
			if err != nil {
				return htaccess.FileInfo{}, false
			}
			defer f.Close()
			return htaccess.FileInfo{IsDir: f.Info.IsDir(), Size: f.Info.Size()}, true
		},
	}
	req.Scheme = "http"
	if r.TLS != nil {
		req.Scheme = "https"
	}
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, port, err := net.SplitHostPort(la.String()); err == nil {
			req.ServerPort = port
		}
	}
	return htResult{dec: htaccess.Evaluate(layers, req, noRewrite)}
}

// filesWithIndex returns a static handler for root with the index list a
// .htaccess DirectoryIndex chose.
func (rt *siteRuntime) filesWithIndex(root string, index []string) *staticfiles.Handler {
	if len(index) == 0 {
		// DirectoryIndex disabled: a name that never resolves, so a directory
		// request is refused instead of falling back to index.html.
		index = []string{".lotus-no-index"}
	}
	key := filesKey(root, index)
	if h := rt.handlers[key]; h != nil {
		return h
	}
	if v, ok := rt.extraHandlers.Load(key); ok {
		return v.(*staticfiles.Handler)
	}
	res := rt.resolvers[root]
	if res == nil {
		return nil
	}
	h := staticfiles.New(res, staticfiles.Options{Index: index, Logger: rt.log})
	rt.extraHandlers.Store(key, h)
	return h
}

// ---- error documents ----

// Error pages of the router are "/path" or "@name". ErrorDocument adds two
// more forms, kept in the same map with a marker byte no path can start with.
const (
	textPagePrefix = "\x00text:"
	urlPagePrefix  = "\x00url:"
)

// overlayPages returns pages with the .htaccess error documents on top. The
// map it was given is not changed.
func overlayPages(pages map[int]string, docs map[int]htaccess.ErrorDoc) map[int]string {
	if len(docs) == 0 {
		return pages
	}
	out := make(map[int]string, len(pages)+len(docs))
	for k, v := range pages {
		out[k] = v
	}
	for code, d := range docs {
		switch d.Kind {
		case htaccess.DocPath:
			out[code] = d.Value
		case htaccess.DocText:
			out[code] = textPagePrefix + d.Value
		case htaccess.DocURL:
			out[code] = urlPagePrefix + d.Value
		}
	}
	return out
}

// ---- headers ----

// htWriter applies .htaccess `Header` directives when the response starts, so
// they see the headers the script or file handler set: `Header unset
// X-Powered-By` must remove PHP's header, and `Header append` must add to it.
type htWriter struct {
	http.ResponseWriter
	ops       []htaccess.HeaderOp
	protected map[string]bool
	done      bool
}

func newHTWriter(w http.ResponseWriter, ops []htaccess.HeaderOp, protected map[string]bool) http.ResponseWriter {
	if len(ops) == 0 {
		return w
	}
	return &htWriter{ResponseWriter: w, ops: ops, protected: protected}
}

func (w *htWriter) apply() {
	if w.done {
		return
	}
	w.done = true
	h := w.ResponseWriter.Header()
	for _, op := range w.ops {
		name := http.CanonicalHeaderKey(op.Name)
		if w.protected[name] {
			continue // the administrator's security_headers can't be changed by a site file
		}
		switch op.Op {
		case "set":
			h.Set(name, op.Value)
		case "setifempty":
			if h.Get(name) == "" {
				h.Set(name, op.Value)
			}
		case "add":
			h.Add(name, op.Value)
		case "append":
			if cur := h.Get(name); cur != "" {
				h.Set(name, cur+", "+op.Value)
			} else {
				h.Set(name, op.Value)
			}
		case "merge":
			cur := h.Get(name)
			switch {
			case cur == "":
				h.Set(name, op.Value)
			case !containsToken(cur, op.Value):
				h.Set(name, cur+", "+op.Value)
			}
		case "unset":
			h.Del(name)
		}
	}
}

func containsToken(list, tok string) bool {
	for _, t := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(t), tok) {
			return true
		}
	}
	return false
}

func (w *htWriter) WriteHeader(code int) {
	w.apply()
	w.ResponseWriter.WriteHeader(code)
}

func (w *htWriter) Write(p []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(p)
}

// ReadFrom keeps the zero-copy path (sendfile) available.
func (w *htWriter) ReadFrom(src io.Reader) (int64, error) {
	w.apply()
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{w.ResponseWriter}, src)
}

func (w *htWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
