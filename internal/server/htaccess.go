package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fspath"
	"github.com/laikait/lotus/internal/htaccess"
	"github.com/laikait/lotus/internal/ratelimit"
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

	lock *ratelimit.Lockout // wrong Basic attempts per client address

	pwMu   sync.Mutex
	pwFile map[string]*htPasswd // password files by path
	hashes chan struct{}        // limits password checks running at once
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
		pwFile: map[string]*htPasswd{}, hashes: make(chan struct{}, 4),
		lock: ratelimit.NewLockout(cfg.AuthFailures, time.Duration(cfg.AuthLockout), time.Duration(cfg.AuthLockout)),
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
	fail       string // not empty: answer 500
	dec        *htaccess.Decision
	retryAfter time.Duration // with status 429: when to try again
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
	// A client that is locked out for wrong passwords gets 429 where it would
	// have been asked for a password again; its password is not even checked.
	key := ratelimit.Key(client)
	locked, left := rt.ht.lock.Locked(key, time.Now())
	req.BasicUser = func(file, dirURL string) string {
		if locked {
			return ""
		}
		return rt.ht.basicUser(root, r.Header.Get("Authorization"), file, dirURL, key)
	}
	dec := htaccess.Evaluate(layers, req, noRewrite)
	if locked && dec.Status == http.StatusUnauthorized {
		dec.Status, dec.AuthRealm = http.StatusTooManyRequests, ""
		return htResult{dec: dec, retryAfter: left}
	}
	if dec.Why != "" && rt.ht.once("why|"+cur.path+"|"+dec.Why) {
		rt.ht.log.Error("htaccess: "+dec.Why, "path", cur.path)
	}
	return htResult{dec: dec}
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

// htWriter applies what .htaccess says about the response when it starts, so it
// sees the headers the script or file handler set: `Header unset X-Powered-By`
// must remove PHP's header, `Header append` must add to it, and an Expires
// lifetime must not override one the script chose.
type htWriter struct {
	http.ResponseWriter
	ops       []htaccess.HeaderOp
	policy    *htaccess.ResponsePolicy
	path      string
	protected map[string]bool
	script    bool // the response comes from a PHP script
	done      bool
}

func newHTWriter(w http.ResponseWriter, dec *htaccess.Decision, protected map[string]bool, path string) http.ResponseWriter {
	if len(dec.Headers) == 0 && dec.Policy.Empty() {
		return w
	}
	return &htWriter{ResponseWriter: w, ops: dec.Headers, policy: dec.Policy, path: path, protected: protected}
}

// markScript tells the .htaccess writer (if the response has one) that a PHP
// script produces it: AddType is about static files and must not rewrite a
// script's own Content-Type.
func markScript(w http.ResponseWriter) {
	for {
		if hw, ok := w.(*htWriter); ok {
			hw.script = true
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

func (w *htWriter) apply(status int) {
	if w.done {
		return
	}
	w.done = true
	h := w.ResponseWriter.Header()
	if !w.policy.Empty() {
		// The administrator's security_headers stay as they are.
		saved := map[string][]string{}
		for _, n := range []string{"Content-Type", "Cache-Control", "Expires"} {
			if w.protected[n] {
				saved[n] = append([]string(nil), h.Values(n)...)
			}
		}
		w.policy.Apply(h, w.path, status, w.script, time.Now())
		for n, v := range saved {
			h.Del(n)
			for _, x := range v {
				h.Add(n, x)
			}
		}
	}
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
	w.apply(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *htWriter) Write(p []byte) (int, error) {
	w.apply(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

// ReadFrom keeps the zero-copy path (sendfile) available.
func (w *htWriter) ReadFrom(src io.Reader) (int64, error) {
	w.apply(http.StatusOK)
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{w.ResponseWriter}, src)
}

func (w *htWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ---- HTTP Basic authentication ----

// once reports true the first time it is called with a key, so a busy site
// doesn't repeat the same complaint on every request.
func (h *htRuntime) once(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.logged[key] {
		return false
	}
	if len(h.logged) > maxHTCache {
		h.logged = map[string]bool{}
	}
	h.logged[key] = true
	return true
}

// basicChallenge is the WWW-Authenticate value for a realm.
func basicChallenge(realm string) string {
	realm = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(realm)
	return `Basic realm="` + realm + `", charset="UTF-8"`
}

// htPasswd is a password file that was read.
type htPasswd struct {
	checked time.Time
	mtime   time.Time
	size    int64
	users   map[string]string
}

// passwordFile finds the file AuthUserFile names: an absolute path, or one
// relative to the directory of the .htaccess that named it. It must lie inside
// the document root (after symlinks), so a site file can't aim the server at
// files of other sites or of the system. Because `.ht*` files are never
// served, a password file named .htpasswd inside the root is safe.
func passwordFile(root, dirURL, file string) (string, bool) {
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, filepath.FromSlash(strings.Trim(dirURL, "/")), filepath.FromSlash(file))
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootReal, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return real, true
}

// users returns the entries of a password file, read again when it changed.
func (h *htRuntime) users(root, dirURL, file string) map[string]string {
	p, ok := passwordFile(root, dirURL, file)
	if !ok {
		if h.once("pw-path|" + root + "|" + dirURL + "|" + file) {
			h.log.Error("htaccess: AuthUserFile is missing or is not inside the site's document root", "directory", dirURL, "file", file)
		}
		return nil
	}
	now := time.Now()
	h.pwMu.Lock()
	cur := h.pwFile[p]
	h.pwMu.Unlock()
	if cur != nil && now.Sub(cur.checked) < h.recheck {
		return cur.users
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > htaccess.MaxHtpasswdSize {
		if h.once("pw-stat|" + p) {
			h.log.Error("htaccess: the password file can't be used (missing, not a regular file, or over 4 MiB)", "file", p)
		}
		return nil
	}
	nu := &htPasswd{checked: now, mtime: fi.ModTime(), size: fi.Size()}
	if cur != nil && cur.mtime.Equal(nu.mtime) && cur.size == nu.size {
		nu.users = cur.users
	} else {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		nu.users = htaccess.ParseHtpasswd(data)
	}
	h.pwMu.Lock()
	if len(h.pwFile) > 256 {
		h.pwFile = map[string]*htPasswd{}
	}
	h.pwFile[p] = nu
	h.pwMu.Unlock()
	return nu.users
}

// basicUser checks the Basic credentials of a request against AuthUserFile and
// returns the user's name, or "" when there are none or they are wrong.
func (h *htRuntime) basicUser(root, authorization, file, dirURL, client string) string {
	user, pass, ok := htaccess.ParseBasic(authorization)
	if !ok {
		return ""
	}
	hash, found := h.users(root, dirURL, file)[user]
	if !found {
		h.lock.Fail(client, time.Now())
		return ""
	}
	// bcrypt is meant to be slow; a flood of requests with bad passwords must
	// not be able to use every CPU of the server.
	h.hashes <- struct{}{}
	defer func() { <-h.hashes }()
	if htaccess.VerifyPassword(hash, pass) {
		h.lock.Reset(client)
		return user
	}
	h.lock.Fail(client, time.Now())
	return ""
}
