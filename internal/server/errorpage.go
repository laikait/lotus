package server

import (
	"io"
	"net/http"
)

// errorWriter sits between a handler and the response. It does two jobs for
// error_page:
//
//   - When a handler starts a response whose status has an error page, the
//     response is dropped (headers restored, body discarded) and intercepted
//     is set, so the router can serve the page instead.
//   - While an error page is served, a successful (200) response is sent with
//     the original error status.
//
// Everything else passes through untouched, including io.ReaderFrom, so file
// responses keep using sendfile.
type errorWriter struct {
	http.ResponseWriter
	pages map[int]string

	disabled bool        // an error page is being served: never intercept
	force    int         // status to send instead of 200
	snapshot http.Header // headers before the handler ran, restored on intercept

	started     bool // a response has begun and goes through
	intercepted bool
	code        int // the intercepted status
}

func newErrorWriter(w http.ResponseWriter, pages map[int]string, es errorState) *errorWriter {
	ew := &errorWriter{ResponseWriter: w, pages: pages, disabled: es.inPage, force: es.force}
	if !ew.disabled {
		ew.snapshot = w.Header().Clone()
	}
	return ew
}

func (e *errorWriter) WriteHeader(code int) {
	if e.started || e.intercepted {
		return
	}
	if !e.disabled {
		if _, ok := e.pages[code]; ok {
			e.intercepted, e.code = true, code
			e.restoreHeaders()
			return
		}
	}
	if e.force != 0 && code == http.StatusOK {
		code = e.force
	}
	e.started = true
	e.ResponseWriter.WriteHeader(code)
}

// restoreHeaders puts back the headers as they were before the handler ran.
func (e *errorWriter) restoreHeaders() {
	h := e.ResponseWriter.Header()
	for k := range h {
		delete(h, k)
	}
	for k, v := range e.snapshot {
		h[k] = v
	}
}

func (e *errorWriter) Write(p []byte) (int, error) {
	if e.intercepted {
		return len(p), nil
	}
	if !e.started {
		e.WriteHeader(http.StatusOK)
		if e.intercepted {
			return len(p), nil
		}
	}
	return e.ResponseWriter.Write(p)
}

// ReadFrom keeps the zero-copy path (sendfile) available.
func (e *errorWriter) ReadFrom(src io.Reader) (int64, error) {
	if e.intercepted {
		return io.Copy(io.Discard, src)
	}
	if !e.started {
		e.WriteHeader(http.StatusOK)
		if e.intercepted {
			return io.Copy(io.Discard, src)
		}
	}
	if rf, ok := e.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{e.ResponseWriter}, src)
}

func (e *errorWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }
