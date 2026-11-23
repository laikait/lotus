package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/fastcgi"
	"github.com/laikait/lotus/internal/staticfiles"
)

// How PHP requests are served (the `php` route action):
//
//   - Only files whose name ends in .php are ever passed to PHP, and only
//     files that exist below the document root and that the file resolver
//     allows (hidden files, symlink policy and so on all apply). A path such as
//     /uploads/evil.jpg/x.php names no file, so it is a 404, not a way to run
//     an uploaded image as PHP.
//   - Everything else under a php route is served as a static file.
//   - A .php file is never sent to a client as text: on a php route it runs;
//     on a route with php_deny, or on any route of a site that uses PHP, asking
//     for it is a 403.

// phpBackend is where PHP requests go: a connection pool to a PHP server that
// runs on its own (fastcgi.Client), or a pool of php-cgi processes LOTUS
// supervises (phpproc.Pool).
type phpBackend interface {
	Do(ctx context.Context, req *fastcgi.Request) (*fastcgi.Response, error)
}

// phpBodyMemory is how much of a request body of unknown length (chunked) is
// kept in memory while waiting for its end. PHP needs CONTENT_LENGTH up front,
// so such a body is read completely first; beyond this it goes to a temporary file.
const phpBodyMemory = 1 << 20

// isPHPName reports whether a file name or path ends in .php, in any letter
// case (a case-insensitive file system makes X.PHP the same file).
func isPHPName(name string) bool { return strings.EqualFold(path.Ext(name), ".php") }

// splitScript splits a URL path at its first segment that ends in .php:
// "/blog/index.php/a/b" becomes "/blog/index.php" and "/a/b".
func splitScript(p string) (script, info string, ok bool) {
	for i := 0; i < len(p); {
		end := len(p)
		if j := strings.IndexByte(p[i+1:], '/'); j >= 0 {
			end = i + 1 + j
		}
		if isPHPName(p[i+1 : end]) {
			return p[:end], p[end:], true
		}
		i = end
	}
	return "", "", false
}

// phpCall is what servePHP needs besides the request.
type phpCall struct {
	backend string
	files   *staticfiles.Handler
	client  string            // the client address, after trusted-proxy resolution
	env     map[string]string // variables set by .htaccess (RewriteRule E flag)
}

// serveFiles serves a file request with the PHP rules that apply to it. It
// says where to route next when the request must be routed again.
func (rt *siteRuntime) serveFiles(final, out http.ResponseWriter, r *http.Request, client string,
	files *staticfiles.Handler, php string, deny bool, env map[string]string) (next target, again bool) {

	switch {
	case deny:
		files.ServeWith(out, r, isPHPName)
	case php != "":
		rt.servePHP(final, out, r, phpCall{backend: php, files: files, client: client, env: env})
	case rt.phpSite:
		// A directory whose index is a PHP file is routed again as that file,
		// so the route that runs PHP gets it (as Nginx does with `index`).
		// Without such a route it comes back here as the file itself and is refused.
		if strings.HasSuffix(r.URL.Path, "/") {
			if o := files.Locate(r.URL.Path, nil); o.Status == http.StatusOK && isPHPName(o.File) {
				return target{path: o.File, query: r.URL.RawQuery}, true
			}
		}
		files.ServeWith(out, r, isPHPName)
	default:
		files.ServeHTTP(out, r)
	}
	return target{}, false
}

// servePHP runs the script a request leads to, or serves a static file when it
// leads to something that is not a script.
//
// Responses of the script are never replaced by error pages (like Nginx
// without fastcgi_intercept_errors): they go to final. Failures of the gateway
// itself (502, 504, 413, ...) go to out, so error pages apply to them.
func (rt *siteRuntime) servePHP(final, out http.ResponseWriter, r *http.Request, c phpCall) {
	if r.Method == http.MethodTrace {
		rt.status(out, r, http.StatusMethodNotAllowed, "")
		return
	}
	client := rt.php[c.backend]
	if client == nil {
		rt.log.Error("php: no such backend", "backend", c.backend)
		rt.status(out, r, http.StatusInternalServerError, "")
		return
	}

	var (
		scriptURL, pathInfo string
		found               bool
	)
	if scriptURL, pathInfo, found = splitScript(r.URL.Path); !found {
		// No script in the path: a directory may lead to an index.php.
		if o := c.files.Locate(r.URL.Path, nil); o.Status == http.StatusOK && isPHPName(o.File) {
			scriptURL, found = o.File, true
		}
	}
	if !found {
		c.files.ServeWith(out, r, isPHPName)
		return
	}
	// The script must be a file that exists and that the resolver allows.
	o := c.files.Locate(scriptURL, nil)
	if o.Status != http.StatusOK || o.File != path.Clean(scriptURL) {
		status := http.StatusNotFound
		if o.Status == http.StatusForbidden || o.Status == http.StatusBadRequest {
			status = o.Status
		}
		rt.status(out, r, status, "")
		return
	}
	scriptURL = o.File

	var stdin io.Reader
	length := r.ContentLength
	if r.Body != nil && r.Body != http.NoBody && length != 0 {
		stdin = r.Body
		if length < 0 {
			spooled, n, cleanup, err := spoolBody(r.Body)
			if err != nil {
				rt.bodyFailure(out, r, err)
				return
			}
			defer cleanup()
			stdin, length = spooled, n
		}
	}

	params := phpParams(phpEnv{
		r:          r,
		client:     c.client,
		root:       c.files.Root(),
		scriptName: scriptURL,
		pathInfo:   pathInfo,
		length:     length,
		extra:      c.env,
	})
	resp, err := client.Do(r.Context(), &fastcgi.Request{Params: params, Stdin: stdin})
	if err != nil {
		rt.phpFailure(out, r, c.backend, scriptURL, err)
		return
	}
	defer resp.Body.Close()
	rt.relayPHP(final, out, r, c.backend, scriptURL, resp)
}

// relayPHP sends the script's response to the client.
func (rt *siteRuntime) relayPHP(final, out http.ResponseWriter, r *http.Request, backend, script string, resp *fastcgi.Response) {
	if resp.Status < 200 {
		rt.phpFailure(out, r, backend, script, errors.New("the script sent an informational status"))
		return
	}
	// Read the first chunk before committing to the status: a backend that dies
	// right after its headers can still become a clean 502.
	buf := make([]byte, 32<<10)
	n, rerr := resp.Body.Read(buf)
	if rerr != nil && rerr != io.EOF && n == 0 {
		rt.phpFailure(out, r, backend, script, rerr)
		return
	}

	copyPHPHeaders(final.Header(), resp.Header)
	final.WriteHeader(resp.Status)
	if r.Method == http.MethodHead {
		return
	}
	// Without a Content-Length the body is streamed: each piece the script
	// produced (it may flush) goes out at once.
	flush := resp.Header.Get("Content-Length") == ""
	rc := http.NewResponseController(final)
	for {
		if n > 0 {
			if _, werr := final.Write(buf[:n]); werr != nil {
				return // the client went away
			}
			if flush {
				_ = rc.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if r.Context().Err() == nil {
				rt.log.Error("php: the backend failed while sending the response", "backend", backend, "script", script, "error", rerr)
			}
			// The status is out already. Cutting the connection is the only way
			// the client can tell the response is incomplete.
			panic(http.ErrAbortHandler)
		}
		n, rerr = resp.Body.Read(buf)
	}
	if msg := resp.Stderr(); msg != "" {
		rt.log.Warn("php: output on stderr", "backend", backend, "script", script, "stderr", msg)
	}
}

// phpFailure answers a request whose script could not be run.
func (rt *siteRuntime) phpFailure(out http.ResponseWriter, r *http.Request, backend, script string, err error) {
	var be *fastcgi.BodyError
	var ne net.Error
	status := http.StatusBadGateway
	switch {
	case errors.As(err, &be):
		rt.bodyFailure(out, r, be.Err)
		return
	case r.Context().Err() != nil:
		// The client is gone; nobody reads the answer. 499 is what Nginx logs.
		out.WriteHeader(499)
		return
	case errors.Is(err, fastcgi.ErrOverloaded):
		status = http.StatusServiceUnavailable
	case errors.As(err, &ne) && ne.Timeout():
		status = http.StatusGatewayTimeout
	}
	attrs := []any{"backend", backend, "script", script, "status", status, "error", err}
	var se *fastcgi.StderrError
	if errors.As(err, &se) {
		attrs = append(attrs, "stderr", se.Stderr)
	}
	rt.log.Error("php: the request failed", attrs...)
	rt.status(out, r, status, "")
}

// bodyFailure answers a request whose body could not be read.
func (rt *siteRuntime) bodyFailure(out http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		rt.status(out, r, http.StatusRequestEntityTooLarge, "")
		return
	}
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		out.WriteHeader(499)
		return
	}
	rt.status(out, r, http.StatusBadRequest, "")
}

// spoolBody reads a body whose length isn't known and returns it with its
// length, in memory when small and from a temporary file otherwise. The caller
// calls cleanup when done.
func spoolBody(body io.Reader) (r io.Reader, size int64, cleanup func(), err error) {
	var mem bytes.Buffer
	n, err := io.Copy(&mem, io.LimitReader(body, phpBodyMemory+1))
	if err != nil {
		return nil, 0, nil, err
	}
	if n <= phpBodyMemory {
		return bytes.NewReader(mem.Bytes()), n, func() {}, nil
	}
	f, err := os.CreateTemp("", "lotus-body-*")
	if err != nil {
		return nil, 0, nil, err
	}
	cleanup = func() {
		f.Close()
		os.Remove(f.Name())
	}
	if _, err = f.Write(mem.Bytes()); err == nil {
		var m int64
		m, err = io.Copy(f, body)
		n += m
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	return f, n, cleanup, nil
}

// phpEnv is what the CGI variables of a request are made from.
type phpEnv struct {
	r          *http.Request
	client     string
	root       string
	scriptName string // URL path of the script
	pathInfo   string
	length     int64 // body length, or -1/0 for none
	// extra are environment variables from .htaccess. They never replace a
	// variable the server sets: a file in the document root can't forge
	// SCRIPT_FILENAME or REMOTE_ADDR.
	extra map[string]string
}

// phpParams builds the CGI environment (RFC 3875) a PHP script expects.
//
// Request headers become HTTP_* variables, except:
//   - Proxy: HTTP_PROXY is read by many HTTP client libraries as the proxy to
//     use for the script's own outgoing requests ("httpoxy").
//   - Headers whose name contains an underscore: X_Foo and X-Foo would both
//     become HTTP_X_FOO, so a client could forge a header that a proxy in front
//     of the server meant to set.
func phpParams(e phpEnv) []fastcgi.Param {
	r := e.r
	scriptFile := filepath.Join(e.root, filepath.FromSlash(strings.TrimPrefix(e.scriptName, "/")))

	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	https, scheme := "", "http"
	if r.TLS != nil {
		https, scheme = "on", "https"
	}
	p := []fastcgi.Param{
		{Name: "GATEWAY_INTERFACE", Value: "CGI/1.1"},
		{Name: "SERVER_SOFTWARE", Value: serverHeader},
		{Name: "SERVER_PROTOCOL", Value: r.Proto},
		{Name: "SERVER_NAME", Value: host},
		{Name: "REQUEST_SCHEME", Value: scheme},
		{Name: "REQUEST_METHOD", Value: r.Method},
		{Name: "REQUEST_URI", Value: r.RequestURI}, // as the client sent it, whatever routing did since
		{Name: "SCRIPT_NAME", Value: e.scriptName},
		{Name: "SCRIPT_FILENAME", Value: scriptFile},
		{Name: "DOCUMENT_ROOT", Value: e.root},
		{Name: "QUERY_STRING", Value: r.URL.RawQuery},
		{Name: "REMOTE_ADDR", Value: e.client},
		{Name: "REDIRECT_STATUS", Value: "200"}, // php-cgi refuses to run without it
	}
	if e.pathInfo != "" {
		p = append(p, fastcgi.Param{Name: "PATH_INFO", Value: e.pathInfo})
	}
	if https != "" {
		p = append(p, fastcgi.Param{Name: "HTTPS", Value: https})
	}
	if _, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		p = append(p, fastcgi.Param{Name: "REMOTE_PORT", Value: port})
	}
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if h, port, err := net.SplitHostPort(la.String()); err == nil {
			p = append(p, fastcgi.Param{Name: "SERVER_ADDR", Value: h}, fastcgi.Param{Name: "SERVER_PORT", Value: port})
		}
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		p = append(p, fastcgi.Param{Name: "CONTENT_TYPE", Value: ct})
	}
	if e.length > 0 || r.Header.Get("Content-Length") != "" {
		n := e.length
		if n < 0 {
			n = 0
		}
		p = append(p, fastcgi.Param{Name: "CONTENT_LENGTH", Value: strconv.FormatInt(n, 10)})
	}
	if r.Host != "" {
		p = append(p, fastcgi.Param{Name: "HTTP_HOST", Value: r.Host})
	}

	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		switch http.CanonicalHeaderKey(name) {
		case "Content-Type", "Content-Length", "Proxy", "Host":
			continue
		}
		if strings.Contains(name, "_") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sep := ", "
		if http.CanonicalHeaderKey(name) == "Cookie" {
			sep = "; "
		}
		p = append(p, fastcgi.Param{
			Name:  "HTTP_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")),
			Value: strings.Join(r.Header[name], sep),
		})
	}
	if len(e.extra) > 0 {
		have := make(map[string]bool, len(p))
		for _, q := range p {
			have[strings.ToUpper(q.Name)] = true
		}
		keys := make([]string, 0, len(e.extra))
		for k := range e.extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !have[strings.ToUpper(k)] {
				p = append(p, fastcgi.Param{Name: k, Value: e.extra[k]})
			}
		}
	}
	return p
}

// phpDropped are response headers of a script that are not passed on:
// hop-by-hop headers belong to the connection between the server and PHP, and
// the X-Accel-*/X-Sendfile family are instructions to a front server that
// LOTUS doesn't follow (passing them on would leak internal paths).
func phpDropped(canonical string) bool {
	switch canonical {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Server", "X-Sendfile":
		return true
	}
	return strings.HasPrefix(canonical, "X-Accel-")
}

// copyPHPHeaders adds the script's headers to the response. A header the
// server already set (the site's security_headers) is not overridden: those
// are the administrator's policy, and a script can't loosen it.
func copyPHPHeaders(dst, src http.Header) {
	preset := make(map[string]bool, len(dst))
	for k := range dst {
		preset[k] = true
	}
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if phpDropped(ck) || preset[ck] {
			continue
		}
		dst[ck] = append(dst[ck], vs...)
	}
}
