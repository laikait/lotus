package cache

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names of the cache.
const statusHeader = "X-Cache"

// Serve answers a request from the cache when it can, and otherwise calls fetch
// (the proxy or the PHP script) with a writer that sees the response, passes it
// to w and keeps a copy when the rules allow.
//
// fetch must write to the writer it is given. It may write nothing at all (a
// failure that goes to an error page instead); then nothing is kept.
func (s *Store) Serve(w http.ResponseWriter, r *http.Request, k Key, o Options, fetch func(http.ResponseWriter)) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		// What a client changes makes the stored reads of that URL stale
		// (RFC 9111 section 4.4), if the change went through.
		sw := &statusWriter{ResponseWriter: w}
		fetch(sw)
		if code := sw.code(); code >= 200 && code < 400 {
			s.Invalidate(k)
		}
		return
	default:
		fetch(w)
		return
	}

	if skip(r, o) {
		s.bypass.Add(1)
		s.run(w, r, k, o, "BYPASS", nil, nil, false, fetch)
		return
	}
	e, st := s.lookup(k, r.Header, s.now())
	if st == Fresh {
		s.hit.Add(1)
		serveEntry(w, r, e, s.now(), "HIT")
		return
	}
	var stale *Entry
	if st == Stale {
		stale = e
	}

	// Only whole responses to plain GETs are worth storing: a HEAD has no body
	// and a Range request gets a part.
	if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
		s.miss.Add(1)
		s.run(w, r, k, o, "MISS", stale, nil, false, fetch)
		return
	}
	if o.LockTimeout <= 0 {
		s.miss.Add(1)
		s.run(w, r, k, o, "MISS", stale, nil, true, fetch)
		return
	}

	fl, leader := s.joinFlight(k)
	if !leader {
		// Somebody is fetching this already: wait for the result instead of
		// asking the backend again.
		timer := time.NewTimer(o.LockTimeout)
		select {
		case <-fl.done:
			timer.Stop()
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			w.WriteHeader(499) // the client is gone
			return
		}
		e, st = s.lookup(k, r.Header, s.now())
		switch {
		case st == Fresh:
			s.hit.Add(1)
			serveEntry(w, r, e, s.now(), "HIT")
			return
		case st == Stale && fl.failed.Load():
			s.stale.Add(1)
			serveEntry(w, r, e, s.now(), "STALE")
			return
		}
		s.miss.Add(1)
		var stale *Entry
		if st == Stale {
			stale = e
		}
		s.run(w, r, k, o, "MISS", stale, nil, false, fetch) // not stored: the leader had its chance
		return
	}
	defer fl.finish()
	s.miss.Add(1)
	s.run(w, r, k, o, "MISS", stale, fl, true, fetch)
}

// run calls fetch with a capturing writer and keeps what it produced.
func (s *Store) run(w http.ResponseWriter, r *http.Request, k Key, o Options, label string,
	stale *Entry, fl *flight, store bool, fetch func(http.ResponseWriter)) {

	c := &capture{
		w: w, r: r, s: s, k: k, o: o, label: label, hdr: http.Header{},
		stale: stale, fl: fl, storing: store && r.Method == http.MethodGet, declared: -1,
	}
	defer c.release()
	fetch(c)
	c.finish()
}

// serveEntry writes a stored response.
func serveEntry(w http.ResponseWriter, r *http.Request, e *Entry, now time.Time, label string) {
	h := w.Header()
	commitHeaders(h, e.header)
	h.Set(statusHeader, label)
	if age := int64(now.Sub(e.stored) / time.Second); age > 0 {
		h.Set("Age", strconv.FormatInt(age, 10))
	}
	if e.status == http.StatusOK {
		// ServeContent adds Content-Length and Accept-Ranges, answers Range and
		// If-Range, and turns If-None-Match / If-Modified-Since into 304.
		http.ServeContent(w, r, "", e.lastMod, bytes.NewReader(e.body))
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(e.body)))
	w.WriteHeader(e.status)
	if r.Method != http.MethodHead && e.status != http.StatusNoContent {
		_, _ = w.Write(e.body)
	}
}

// commitHeaders copies a response's headers into the writer's, leaving alone
// the names the server has set itself (its Server header, the security
// headers): the backend doesn't get to override those.
func commitHeaders(dst, src http.Header) {
	preset := make(map[string]bool, len(dst))
	for k := range dst {
		preset[k] = true
	}
	for k, v := range src {
		if !preset[k] {
			dst[k] = append([]string(nil), v...)
		}
	}
}

// dropped are headers that describe one transmission, not the response.
var dropped = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Connection": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Content-Length": true, "Date": true, "Age": true, statusHeader: true,
}

func isServerError(code int) bool {
	return code == http.StatusInternalServerError || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// capture is the writer a handler writes its response to. It passes the
// response on to the client, and keeps a copy for the cache when allowed.
type capture struct {
	w     http.ResponseWriter
	r     *http.Request
	s     *Store
	k     Key
	o     Options
	label string
	hdr   http.Header // what the handler sets; copied into w's header when the status is written

	wrote   bool
	status  int
	discard bool   // the backend's response is dropped (a stale entry was served instead)
	stale   *Entry // an entry past its lifetime that may be served if the backend fails
	fl      *flight

	storing  bool
	declared int64 // Content-Length, or -1
	snap     http.Header
	ttl      time.Duration
	noStale  bool
	vary     []string
	buf      []byte
	size     int64
	reserved int64
	failed   bool // writing to the client failed: the copy is incomplete
	madeAt   time.Time
}

func (c *capture) Header() http.Header { return c.hdr }

// Unwrap lets http.ResponseController reach the real writer (deadlines).
func (c *capture) Unwrap() http.ResponseWriter { return c.w }

func (c *capture) WriteHeader(code int) {
	if c.wrote {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		c.informational(code)
		return
	}
	c.wrote, c.status = true, code
	if c.stale != nil && isServerError(code) && c.useStale() {
		return
	}
	if c.storing {
		c.decide()
	}
	if !c.storing && c.fl != nil {
		c.fl.finish() // it won't be in the cache: nobody should wait for it
	}
	commitHeaders(c.w.Header(), c.hdr)
	c.w.Header().Set(statusHeader, c.label)
	c.w.WriteHeader(code)
}

// informational passes a 1xx response (such as 103 Early Hints) on.
func (c *capture) informational(code int) {
	real := c.w.Header()
	var added []string
	for k, v := range c.hdr {
		if _, ok := real[k]; !ok {
			real[k] = append([]string(nil), v...)
			added = append(added, k)
		}
	}
	c.w.WriteHeader(code)
	for _, k := range added {
		delete(real, k)
	}
}

func (c *capture) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if c.discard {
		return len(p), nil
	}
	n, err := c.w.Write(p)
	if err != nil {
		c.failed = true
	}
	if c.storing && n > 0 {
		c.append(p[:n])
	}
	return n, err
}

// Flush sends what has been written so far. A flush alone doesn't stop the
// copy: a proxy flushes responses whose length it doesn't know after every
// write.
func (c *capture) Flush() { _ = c.FlushError() }

func (c *capture) FlushError() error {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if c.discard {
		return nil
	}
	return http.NewResponseController(c.w).Flush()
}

// ServeStale answers with the stale entry, if there is one and nothing has been
// written yet. A proxy calls it when the backend could not be reached.
func (c *capture) ServeStale() bool {
	if c.wrote || c.stale == nil {
		return false
	}
	c.wrote = true
	return c.useStale()
}

func (c *capture) useStale() bool {
	if c.stale == nil {
		return false
	}
	c.discard = true
	c.storing = false
	if c.fl != nil {
		c.fl.failed.Store(true)
		c.fl.finish()
	}
	c.s.stale.Add(1)
	serveEntry(c.w, c.r, c.stale, c.s.now(), "STALE")
	return true
}

// decide works out, when the status is written, whether the response is kept.
func (c *capture) decide() {
	now := c.s.now()
	h := c.hdr
	ttl, noStale, ok := freshness(c.status, h, now, c.o)
	names, vok := varyOf(h)
	if !ok || !vok {
		c.storing = false
		return
	}
	if cl := strings.TrimSpace(h.Get("Content-Length")); cl != "" {
		n, err := strconv.ParseInt(cl, 10, 64)
		if err != nil || n < 0 || n > c.s.maxEntry.Load() {
			c.storing = false
			return
		}
		c.declared = n
	}
	c.snap, c.ttl, c.noStale, c.vary, c.madeAt = h.Clone(), ttl, noStale, names, now
}

// append copies body bytes, giving up when the response is too big or too many
// responses are being copied at once.
func (c *capture) append(p []byte) {
	n := int64(len(p))
	if c.size+n > c.s.maxEntry.Load() {
		c.abandon()
		return
	}
	if c.s.capturing.Add(n) > c.s.limit() {
		c.s.capturing.Add(-n)
		c.abandon()
		return
	}
	c.reserved += n
	c.size += n
	c.buf = append(c.buf, p...)
}

// abandon stops copying: the response is passed on but not kept.
func (c *capture) abandon() {
	c.storing = false
	c.buf = nil
	c.s.capturing.Add(-c.reserved)
	c.reserved = 0
	if c.fl != nil {
		c.fl.finish()
	}
}

func (c *capture) release() {
	if c.reserved > 0 {
		c.s.capturing.Add(-c.reserved)
		c.reserved = 0
	}
}

// finish runs when the handler has returned: trailers are passed on and a
// complete, cacheable response is stored.
func (c *capture) finish() {
	if c.wrote && !c.discard {
		c.relayTrailers()
	}
	if !c.wrote || !c.storing || c.failed || c.discard {
		return
	}
	if c.declared >= 0 && c.size != c.declared {
		return // the body was cut short
	}
	h := c.snap
	for name := range dropped {
		h.Del(name)
	}
	e := &Entry{
		status: c.status,
		header: h,
		body:   bytes.Clone(c.buf), // exactly the size that is counted, without the slack of append
		stored: c.madeAt,
		fresh:  c.madeAt.Add(c.ttl),
		vary:   c.vary,
		host:   c.k.Host, path: c.k.Path, variant: c.k.variant(), pk: c.k.pathKey(),
		noStale: c.noStale,
	}
	if age := seconds(strings.TrimSpace(c.hdr.Get("Age"))); age > 0 {
		e.stored = c.madeAt.Add(-age)
	}
	e.until = e.fresh
	if !c.noStale && c.o.StaleIfError > 0 {
		e.until = e.fresh.Add(c.o.StaleIfError)
	}
	if lm, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
		e.lastMod = lm
	}
	e.varyVal = varyValue(e.vary, c.r.Header)
	c.buf = nil
	c.s.add(e)
}

// relayTrailers passes on trailers the handler set after the body.
func (c *capture) relayTrailers() {
	announced := map[string]bool{}
	for _, v := range c.hdr.Values("Trailer") {
		for _, n := range strings.Split(v, ",") {
			announced[http.CanonicalHeaderKey(strings.TrimSpace(n))] = true
		}
	}
	real := c.w.Header()
	for k, v := range c.hdr {
		if strings.HasPrefix(k, http.TrailerPrefix) || announced[k] {
			real[k] = append([]string(nil), v...)
		}
	}
}

// statusWriter remembers the status of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) code() int { return w.status }
