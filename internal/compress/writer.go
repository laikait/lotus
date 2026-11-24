package compress

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Config is the compression settings that apply to a response.
type Config struct {
	// Algorithms are the codings offered, in the server's order of preference.
	Algorithms []string
	// Types are the media types worth compressing: "text/html", or "text/*".
	Types []string
	// MinSize is the smallest body that is compressed.
	MinSize int64
	Level   Level
	// Limiter bounds simultaneous compressions; nil means no bound.
	Limiter *Limiter
	// Observe, when set, is told about every response that was compressed: the
	// coding and the body size before and after.
	Observe func(coding string, in, out int64)

	once     sync.Once
	exact    map[string]bool
	prefixes []string
}

func (c *Config) prepare() {
	c.once.Do(func() {
		c.exact = map[string]bool{}
		for _, t := range c.Types {
			t = strings.ToLower(strings.TrimSpace(t))
			if p, ok := strings.CutSuffix(t, "/*"); ok {
				c.prefixes = append(c.prefixes, p+"/")
			} else {
				c.exact[t] = true
			}
		}
	})
}

// Compressible reports whether a media type (without parameters, lower case) is
// one of the configured types.
func (c *Config) Compressible(mediaType string) bool {
	c.prepare()
	// An event stream is about delivering each event at once; "text/*" doesn't
	// include it, whatever the list says.
	if mediaType == "text/event-stream" {
		return false
	}
	if c.exact[mediaType] {
		return true
	}
	for _, p := range c.prefixes {
		if strings.HasPrefix(mediaType, p) {
			return true
		}
	}
	return false
}

// DefaultTypes are the media types that shrink well. Images, video, audio,
// archives and fonts in WOFF are already compressed and are not here.
var DefaultTypes = []string{
	"text/*",
	"application/json", "application/ld+json", "application/manifest+json", "application/geo+json",
	"application/javascript", "application/x-javascript", "application/xml", "application/xhtml+xml",
	"application/rss+xml", "application/atom+xml", "application/wasm",
	"image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon",
	"font/ttf", "font/otf", "application/vnd.ms-fontobject",
}

// state of a Writer.
const (
	stUndecided = iota
	stPass      // the response goes through as it is
	stPending   // compressible, length unknown: waiting to see whether it is big enough
	stCompress
)

// Writer compresses the response written to it when the rules allow, and passes
// it on unchanged when they don't. Create one per request with New, and end it
// with Close (or Abort when the handler failed half way).
type Writer struct {
	w      http.ResponseWriter
	r      *http.Request
	cfg    *Config
	accept []string

	state  int
	wrote  bool
	status int
	slot   bool   // a Limiter slot is held
	buf    []byte // pending body while the size is unknown

	enc    encoder
	coding string
	in     int64
	out    countWriter
}

// New returns a Writer for the request. A request whose client accepts none of
// the configured codings still gets one: it adds Vary where a response could
// have been compressed.
func New(w http.ResponseWriter, r *http.Request, cfg *Config) *Writer {
	return &Writer{w: w, r: r, cfg: cfg, accept: Negotiate(r.Header.Get("Accept-Encoding"), cfg.Algorithms)}
}

// Header returns the response headers.
func (c *Writer) Header() http.Header { return c.w.Header() }

// Unwrap lets http.ResponseController reach the real writer.
func (c *Writer) Unwrap() http.ResponseWriter { return c.w }

func mediaType(ct string) string {
	ct, _, _ = strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

func hasToken(header, token string) bool {
	for _, p := range strings.Split(header, ",") {
		name, _, _ := strings.Cut(p, "=")
		if strings.EqualFold(strings.TrimSpace(name), token) {
			return true
		}
	}
	return false
}

// addVary adds a name to Vary, unless it is there already or Vary is "*".
func addVary(h http.Header, name string) {
	for _, line := range h.Values("Vary") {
		for _, v := range strings.Split(line, ",") {
			v = strings.TrimSpace(v)
			if v == "*" || strings.EqualFold(v, name) {
				return
			}
		}
	}
	h.Add("Vary", name)
}

// noBodyStatus lists statuses whose responses are not compressed: they have no
// body, or part of one.
func noBodyStatus(code int) bool {
	return code < 200 || code == 204 || code == 205 || code == 206 || code == 304
}

// WriteHeader decides whether the response is compressed, and writes its header.
func (c *Writer) WriteHeader(code int) {
	if c.wrote {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		c.w.WriteHeader(code) // informational: not the response yet
		return
	}
	c.wrote, c.status = true, code
	c.decide()
}

func (c *Writer) pass() {
	c.state = stPass
	c.releaseSlot()
	c.w.WriteHeader(c.status)
}

func (c *Writer) releaseSlot() {
	if c.slot {
		c.slot = false
		c.cfg.Limiter.release()
	}
}

func (c *Writer) decide() {
	h := c.w.Header()
	ct := mediaType(h.Get("Content-Type"))
	if noBodyStatus(c.status) || c.status == http.StatusSwitchingProtocols ||
		h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" ||
		hasToken(strings.Join(h.Values("Cache-Control"), ","), "no-transform") ||
		ct == "" || !c.cfg.Compressible(ct) {
		c.pass()
		return
	}
	// From here the answer depends on Accept-Encoding, whether or not this client
	// gets a compressed one: caches in between must know.
	addVary(h, "Accept-Encoding")
	if len(c.accept) == 0 {
		c.pass()
		return
	}
	cl := int64(-1)
	if v := strings.TrimSpace(h.Get("Content-Length")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cl = n
		}
	}
	if (cl >= 0 && cl < c.cfg.MinSize) || (cl < 0 && c.r.Method == http.MethodHead) {
		c.pass()
		return
	}
	if l := c.cfg.Limiter; l != nil {
		if !l.tryAcquire() {
			c.pass()
			return
		}
		c.slot = true
	}
	c.coding = c.accept[0]
	if cl >= 0 {
		c.start()
		return
	}
	c.state = stPending
}

// start commits to compression: the headers change and the header is written.
func (c *Writer) start() {
	h := c.w.Header()
	h.Set("Content-Encoding", c.coding)
	h.Del("Content-Length")
	h.Del("Accept-Ranges") // a range of the compressed bytes is of no use to the client
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et) // the bytes differ from the uncompressed representation
	}
	c.w.WriteHeader(c.status)
	if c.r.Method == http.MethodHead {
		c.state = stPass
		c.releaseSlot()
		return
	}
	c.out = countWriter{w: c.w}
	c.enc = getEncoder(c.coding, c.cfg.Level, &c.out)
	c.state = stCompress
}

// flushPending turns a waiting response into a compressed one.
func (c *Writer) flushPending() error {
	c.start()
	if c.state != stCompress {
		return nil
	}
	buf := c.buf
	c.buf = nil
	if len(buf) == 0 {
		return nil
	}
	n, err := c.enc.Write(buf)
	c.in += int64(n)
	return err
}

// Write sends body bytes, compressed or not.
func (c *Writer) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	switch c.state {
	case stCompress:
		n, err := c.enc.Write(p)
		c.in += int64(n)
		return n, err
	case stPending:
		c.buf = append(c.buf, p...)
		if int64(len(c.buf)) >= c.cfg.MinSize {
			if err := c.flushPending(); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	}
	return c.w.Write(p)
}

// ReadFrom keeps zero-copy file transfers (sendfile) available for responses
// that are not compressed.
func (c *Writer) ReadFrom(src io.Reader) (int64, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if c.state == stPass {
		if rf, ok := c.w.(io.ReaderFrom); ok {
			return rf.ReadFrom(src)
		}
		return io.Copy(writerOnly{c.w}, src)
	}
	return io.Copy(writerOnly{c}, src)
}

// Flush sends what has been written. A response of unknown length starts being
// compressed at the first flush: it is a stream, whatever its size so far.
func (c *Writer) Flush() { _ = c.FlushError() }

// FlushError is Flush that reports the error.
func (c *Writer) FlushError() error {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if c.state == stPending {
		if err := c.flushPending(); err != nil {
			return err
		}
	}
	if c.state == stCompress {
		if err := c.enc.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(c.w).Flush()
}

// Close ends the response: a response that never grew to MinSize goes out as it
// is, a compressed one gets its end marker. Call it when the handler returns.
func (c *Writer) Close() error {
	var err error
	switch c.state {
	case stPending:
		buf := c.buf
		c.buf = nil
		c.pass()
		if len(buf) > 0 {
			_, err = c.w.Write(buf)
		}
	case stCompress:
		err = c.enc.Close()
		putEncoder(c.coding, c.cfg.Level, c.enc)
		c.enc = nil
		c.state = stPass
		c.releaseSlot()
		if c.cfg.Observe != nil {
			c.cfg.Observe(c.coding, c.in, c.out.n)
		}
	}
	return err
}

// Abort ends a response that failed half way. No end marker is written, so the
// client can tell the body is incomplete (the connection is cut by the caller).
func (c *Writer) Abort() {
	if c.state == stCompress && c.enc != nil {
		putEncoder(c.coding, c.cfg.Level, c.enc)
		c.enc = nil
	}
	c.buf = nil
	c.state = stPass
	c.releaseSlot()
}

// Coding is the coding used for the response, or "" when it is not compressed
// (so far).
func (c *Writer) Coding() string {
	if c.state == stCompress {
		return c.coding
	}
	return ""
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writerOnly hides ReadFrom so that io.Copy can't call it again.
type writerOnly struct{ io.Writer }
