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

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	f, err := h.res.Open(r.URL.Path)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer f.Close()

	if f.Info.IsDir() {
		h.serveDir(w, r, f.Rel)
		return
	}
	h.serveFile(w, r, f)
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

// serveDir redirects to the canonical directory URL and serves the first index
// file that exists. Directory listings are not supported.
func (h *Handler) serveDir(w http.ResponseWriter, r *http.Request, rel string) {
	if !strings.HasSuffix(r.URL.Path, "/") {
		u := url.URL{Path: path.Base(r.URL.Path) + "/", RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
		return
	}
	base := "/"
	if rel != "" && rel != "." {
		base = "/" + rel + "/"
	}
	for _, name := range h.index {
		f, err := h.res.Open(base + name)
		if err != nil {
			if errors.Is(err, fspath.ErrNotFound) || errors.Is(err, fspath.ErrForbidden) {
				continue
			}
			h.fail(w, r, err)
			return
		}
		if f.Info.IsDir() {
			f.Close()
			continue
		}
		defer f.Close()
		h.serveFile(w, r, f)
		return
	}
	http.Error(w, "forbidden", http.StatusForbidden) // no index file and listings are off
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

// fail maps resolver errors to HTTP statuses. The response never reveals file
// system paths.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, fspath.ErrNotFound):
		http.Error(w, "404 page not found", http.StatusNotFound)
	case errors.Is(err, fspath.ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, fspath.ErrBadPath):
		http.Error(w, "bad request", http.StatusBadRequest)
	default:
		if h.log != nil {
			h.log.Error("static file error", "path", r.URL.Path, "error", err)
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
