package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/laikait/lotus/internal/control"
	"github.com/laikait/lotus/internal/sitefile"
)

//go:embed ui
var uiFiles embed.FS

const (
	cookieName    = "lotus_admin"
	maxBody       = 16 << 10
	maxFailures   = 5                // wrong passwords from one address ...
	failureWindow = 10 * time.Minute // ... within this time lock that address out for the same time
	maxSessions   = 1000
)

// Options configure a Server.
type Options struct {
	Backend    control.Backend
	Sites      *sitefile.Store // the sites.d folder; nil: no site pages
	Users      Users
	SessionTTL time.Duration
	Allow      []netip.Prefix // empty: any address
	Secure     bool           // the listener is HTTPS: the cookie gets the Secure attribute
	Log        *slog.Logger
	Now        func() time.Time // tests set it
}

type session struct {
	user, role, csrf string
	expires          time.Time
}

type failures struct {
	count int
	first time.Time
}

// Server is the admin's HTTP handler and listener.
type Server struct {
	opts Options
	log  *slog.Logger
	http *http.Server

	mu       sync.Mutex
	sessions map[string]*session
	fails    map[string]*failures
}

// New builds a Server. Call Serve to run it.
func New(opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 8 * time.Hour
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	s := &Server{opts: opts, log: opts.Log.With("component", "admin"), sessions: map[string]*session{}, fails: map[string]*failures{}}
	s.http = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return s
}

// Serve accepts connections on ln until Shutdown.
func (s *Server) Serve(ln net.Listener) error {
	err := s.http.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown stops the listener and waits for requests in progress.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// Handler returns the routes, wrapped in the allow list and the security headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /", http.FileServerFS(ui))

	mux.HandleFunc("POST /v1/login", s.login)
	mux.HandleFunc("POST /v1/logout", s.auth(RoleViewer, s.logout))
	mux.HandleFunc("GET /v1/session", s.auth(RoleViewer, s.sessionInfo))
	mux.HandleFunc("GET /v1/status", s.auth(RoleViewer, func(w http.ResponseWriter, r *http.Request, _ *session) {
		writeJSON(w, http.StatusOK, s.opts.Backend.Status())
	}))
	mux.HandleFunc("GET /v1/health", s.auth(RoleViewer, func(w http.ResponseWriter, r *http.Request, _ *session) {
		writeJSON(w, http.StatusOK, s.opts.Backend.Health())
	}))
	mux.HandleFunc("GET /v1/sites", s.auth(RoleViewer, func(w http.ResponseWriter, r *http.Request, _ *session) {
		writeJSON(w, http.StatusOK, s.opts.Backend.Sites())
	}))
	mux.HandleFunc("GET /v1/certs", s.auth(RoleViewer, func(w http.ResponseWriter, r *http.Request, _ *session) {
		writeJSON(w, http.StatusOK, s.opts.Backend.Certificates())
	}))
	mux.HandleFunc("POST /v1/reload", s.auth(RoleAdmin, s.reload))
	if s.opts.Sites != nil {
		mux.HandleFunc("GET /v1/site-files", s.auth(RoleViewer, s.listSiteFiles))
		mux.HandleFunc("POST /v1/site-files", s.auth(RoleAdmin, s.addSite))
		mux.HandleFunc("PUT /v1/site-files/{name}", s.auth(RoleAdmin, s.updateSite))
		mux.HandleFunc("DELETE /v1/site-files/{name}", s.auth(RoleAdmin, s.deleteSite))
	}
	return s.guard(mux)
}

// guard applies the allow list and the headers every response carries.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.opts.Allow) > 0 && !s.allowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowed(r *http.Request) bool {
	ip, ok := clientIP(r)
	if !ok {
		return false
	}
	for _, p := range s.opts.Allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the address of the connection. X-Forwarded-For is not read: the
// admin is reached directly.
func clientIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func remote(r *http.Request) string {
	if ip, ok := clientIP(r); ok {
		return ip.String()
	}
	return "unknown"
}

// auth wraps a handler: it needs a valid session of at least the given role,
// and, for anything but GET, the session's CSRF token in X-CSRF-Token.
func (s *Server) auth(min string, next func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.sessionOf(r)
		if sess == nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.csrf)) != 1 {
				writeError(w, http.StatusForbidden, "missing or wrong CSRF token")
				return
			}
		}
		if min == RoleAdmin && sess.role != RoleAdmin {
			writeError(w, http.StatusForbidden, "this needs the admin role")
			return
		}
		next(w, r, sess)
	}
}

func (s *Server) sessionOf(r *http.Request) *session {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[c.Value]
	if sess == nil {
		return nil
	}
	if !s.opts.Now().Before(sess.expires) {
		delete(s.sessions, c.Value)
		return nil
	}
	return sess
}

func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no randomness: nothing safe can be done
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type loginBody struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := remote(r)
	if s.locked(ip) {
		s.log.Warn("sign-in refused: too many failures", "ip", ip)
		writeError(w, http.StatusTooManyRequests, "too many failed sign-ins; try again later")
		return
	}
	var body loginBody
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "send the name and password as JSON")
		return
	}
	user, ok := s.opts.Users.Verify(body.Name, body.Password)
	if !ok {
		s.fail(ip)
		s.log.Warn("sign-in failed", "ip", ip, "user", truncate(body.Name, 64))
		writeError(w, http.StatusUnauthorized, "wrong name or password")
		return
	}
	sess := &session{user: user.Name, role: user.Role, csrf: token(), expires: s.opts.Now().Add(s.opts.SessionTTL)}
	id := token()
	s.mu.Lock()
	s.expireLocked()
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "too many sessions open")
		return
	}
	s.sessions[id] = sess
	delete(s.fails, ip)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: s.opts.Secure,
		SameSite: http.SameSiteStrictMode, Expires: sess.expires})
	s.log.Info("signed in", "ip", ip, "user", user.Name, "role", user.Role)
	writeJSON(w, http.StatusOK, sessionView(sess))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess *session) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.opts.Secure, SameSite: http.SameSiteStrictMode})
	s.log.Info("signed out", "ip", remote(r), "user", sess.user)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request, sess *session) {
	writeJSON(w, http.StatusOK, sessionView(sess))
}

func sessionView(sess *session) map[string]string {
	return map[string]string{"user": sess.user, "role": sess.role, "csrf": sess.csrf}
}

func (s *Server) reload(w http.ResponseWriter, r *http.Request, sess *session) {
	res, rerr := s.opts.Backend.Reload("")
	if rerr != nil {
		s.log.Warn("reload refused", "ip", remote(r), "user", sess.user, "reason", rerr.Message)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": rerr.Message, "diagnostics": rerr.Data})
		return
	}
	s.log.Info("reloaded", "ip", remote(r), "user", sess.user, "generation", res.Generation)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": res.Generation, "diagnostics": res.Diagnostics})
}

// ---- failed sign-ins and expiry ----

func (s *Server) locked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip]
	if f == nil {
		return false
	}
	if s.opts.Now().Sub(f.first) > failureWindow {
		delete(s.fails, ip)
		return false
	}
	return f.count >= maxFailures
}

func (s *Server) fail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip]
	if f == nil || s.opts.Now().Sub(f.first) > failureWindow {
		if len(s.fails) > 10000 { // an attacker with many addresses must not grow the table without end
			s.fails = map[string]*failures{}
		}
		f = &failures{first: s.opts.Now()}
		s.fails[ip] = f
	}
	f.count++
}

func (s *Server) expireLocked() {
	now := s.opts.Now()
	for id, sess := range s.sessions {
		if !now.Before(sess.expires) {
			delete(s.sessions, id)
		}
	}
}

// ---- JSON ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "message": msg})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
