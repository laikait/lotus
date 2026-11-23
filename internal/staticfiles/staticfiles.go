// Package staticfiles serves files from a document root. Every file lookup
// goes through fspath, so traversal, symlink and hidden-file rules are
// enforced there and not here.
//
// Conditional requests (ETag, Last-Modified), Range requests and HEAD are
// handled by net/http's ServeContent. Files are streamed from disk; because
// the response writer is not wrapped in a way that hides io.ReaderFrom, Go can
// use sendfile (Linux, FreeBSD) or TransmitFile (Windows).
package staticfiles

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/fspath"
)

// Handler serves one document root.
type Handler struct {
	res    *fspath.Resolver
	index  []string
	log    *slog.Logger
	mimeBy map[string]string
}

// Options configure a Handler.
type Options struct {
	// Index lists the files tried, in order, for a request that names a directory.
	Index []string
	// Logger receives unexpected errors (not 404s). Nil discards them.
	Logger *slog.Logger
	// MIME overrides or adds content types by extension, e.g. ".wasm": "application/wasm".
	MIME map[string]string
}

// New creates a handler that serves files from res.
func New(res *fspath.Resolver, opts Options) *Handler {
	h := &Handler{res: res, index: opts.Index, log: opts.Logger, mimeBy: map[string]string{}}
	if len(h.index) == 0 {
		h.index = []string{"index.html"}
	}
	for ext, typ := range opts.MIME {
		h.mimeBy[strings.ToLower(ext)] = typ
	}
	return h
}

// Outcome describes what the handler would do for a request, without doing it.
// ServeHTTP and Describe share one decision (resolve), so they can't disagree.
type Outcome struct {
	Status   int    // 200 for a file, else the error or redirect status
	File     string // the file served, as a URL path (Status 200)
	Redirect string // the Location of a redirect (Status 301)
	Message  string // why, for an error
}

// plan is the decision for one request. A non-nil file must be closed by the caller.
type plan struct {
	Outcome
	file *fspath.File
	err  error // an unexpected error, to be logged
}

// resolve decides what a request for urlPath gets: a file to serve, a
// redirect, or an error status. rawQuery is only carried into redirects.
func (h *Handler) resolve(method, urlPath, rawQuery string) plan {
	if method != http.MethodGet && method != http.MethodHead {
		return plan{Outcome: Outcome{Status: http.StatusMethodNotAllowed, Message: "method not allowed"}}
	}
	f, err := h.res.Open(urlPath)
	if err != nil {
		return h.failure(err)
	}
	if !f.Info.IsDir() {
		return plan{Outcome: Outcome{Status: http.StatusOK, File: "/" + f.Rel}, file: f}
	}
	defer f.Close()

	// A directory: redirect to the canonical URL, then serve its index file.
	// Directory listings are not supported.
	if !strings.HasSuffix(urlPath, "/") {
		u := url.URL{Path: path.Base(urlPath) + "/", RawQuery: rawQuery}
		return plan{Outcome: Outcome{Status: http.StatusMovedPermanently, Redirect: u.String()}}
	}
	base := "/"
	if f.Rel != "" && f.Rel != "." {
		base = "/" + f.Rel + "/"
	}
	for _, name := range h.index {
		idx, err := h.res.Open(base + name)
		if err != nil {
			if errors.Is(err, fspath.ErrNotFound) || errors.Is(err, fspath.ErrForbidden) {
				continue
			}
			return h.failure(err)
		}
		if idx.Info.IsDir() {
			idx.Close()
			continue
		}
		return plan{Outcome: Outcome{Status: http.StatusOK, File: "/" + idx.Rel}, file: idx}
	}
	return plan{Outcome: Outcome{Status: http.StatusForbidden, Message: "forbidden"}}
}

// failure maps resolver errors to HTTP statuses. Messages never reveal file
// system paths.
func (h *Handler) failure(err error) plan {
	switch {
	case errors.Is(err, fspath.ErrNotFound):
		return plan{Outcome: Outcome{Status: http.StatusNotFound, Message: "404 page not found"}}
	case errors.Is(err, fspath.ErrForbidden):
		return plan{Outcome: Outcome{Status: http.StatusForbidden, Message: "forbidden"}}
	case errors.Is(err, fspath.ErrBadPath):
		return plan{Outcome: Outcome{Status: http.StatusBadRequest, Message: "bad request"}}
	}
	return plan{Outcome: Outcome{Status: http.StatusInternalServerError, Message: "internal server error"}, err: err}
}

// Describe reports what ServeHTTP would do for method and urlPath, without
// writing a response.
func (h *Handler) Describe(method, urlPath string) Outcome {
	p := h.resolve(method, urlPath, "")
	if p.file != nil {
		p.file.Close()
	}
	if p.Redirect != "" { // ServeHTTP lets http.Redirect resolve the relative form
		p.Redirect = urlPath + "/"
	}
	return p.Outcome
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := h.resolve(r.Method, r.URL.Path, r.URL.RawQuery)
	switch {
	case p.file != nil:
		defer p.file.Close()
		h.serveFile(w, r, p.file)
	case p.Redirect != "":
		http.Redirect(w, r, p.Redirect, p.Status)
	default:
		if p.Status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", "GET, HEAD")
		}
		if p.err != nil && h.log != nil {
			h.log.Error("static file error", "path", r.URL.Path, "error", p.err)
		}
		http.Error(w, p.Message, p.Status)
	}
}

// Stat reports whether urlPath names an existing file or directory that the
// resolver would let ServeHTTP open. Paths the policy forbids (hidden files,
// escaping symlinks) and unexpected errors count as "not found", so a
// try_files list falls through to its next candidate instead of revealing that
// a forbidden file exists.
func (h *Handler) Stat(urlPath string) (isDir, exists bool) {
	f, err := h.res.Open(urlPath)
	if err != nil {
		return false, false
	}
	defer f.Close()
	return f.Info.IsDir(), true
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, f *fspath.File) {
	hdr := w.Header()
	if hdr.Get("Content-Type") == "" {
		if typ := h.contentType(f.Rel); typ != "" {
			hdr.Set("Content-Type", typ)
		}
	}
	if hdr.Get("Etag") == "" {
		hdr.Set("Etag", etag(f))
	}
	http.ServeContent(w, r, f.Info.Name(), f.Info.ModTime(), f.File)
}

// etag is a weak validator built from size and modification time.
func etag(f *fspath.File) string {
	return fmt.Sprintf(`W/"%s-%s"`,
		strconv.FormatInt(f.Info.Size(), 16), strconv.FormatInt(f.Info.ModTime().UnixNano(), 16))
}

func (h *Handler) contentType(rel string) string {
	ext := strings.ToLower(path.Ext(rel))
	if ext == "" {
		return ""
	}
	if t, ok := h.mimeBy[ext]; ok {
		return t
	}
	return mime.TypeByExtension(ext) // empty if unknown: ServeContent then sniffs
}
