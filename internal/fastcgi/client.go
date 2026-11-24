package fastcgi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors a caller may want to tell apart (all mean "bad gateway" to a client).
var (
	// ErrNoResponse: the backend ended the request without sending any output,
	// as when the script crashes. Response.Stderr() usually says why.
	ErrNoResponse = errors.New("fastcgi: the backend produced no response")
	// ErrBackendClosed: the connection closed before the response was complete.
	ErrBackendClosed = errors.New("fastcgi: the backend closed the connection before the response was complete")
	// ErrMalformedResponse: the backend's CGI headers are invalid.
	ErrMalformedResponse = errors.New("fastcgi: the backend sent invalid CGI headers")
	// ErrOverloaded: the backend refused the request (FCGI_OVERLOADED or
	// FCGI_CANT_MPX_CONN).
	ErrOverloaded = errors.New("fastcgi: the backend is overloaded")
	// ErrBadParam: a parameter that can't be sent safely.
	ErrBadParam = errors.New("fastcgi: invalid parameter")
)

// DialError is a failure to connect to the backend. Nothing was sent and the
// request body was not read, so the request can safely be tried again, on
// another backend for instance.
type DialError struct{ Err error }

func (e *DialError) Error() string { return "fastcgi: connecting to the backend: " + e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

// BodyError wraps an error from reading the request body that was being sent
// to the backend (for example a client that disconnected, or a body over the
// size limit), as opposed to a failure of the backend itself.
type BodyError struct{ Err error }

func (e *BodyError) Error() string { return "reading the request body: " + e.Err.Error() }
func (e *BodyError) Unwrap() error { return e.Err }

// StderrError is a failure together with what the backend wrote to stderr
// before it (the PHP error message, for php-cgi). Use errors.Is / errors.As to
// look inside.
type StderrError struct {
	Err    error
	Stderr string
}

func (e *StderrError) Error() string { return e.Err.Error() }
func (e *StderrError) Unwrap() error { return e.Err }

// senderGrace is how long Body.Close waits for the goroutine that sends the
// request to return before treating the connection as unusable.
const senderGrace = 50 * time.Millisecond

// Limits that protect the server from a misbehaving backend or request.
const (
	maxHeaderBytes = 64 << 10 // CGI response headers
	maxStderrBytes = 64 << 10 // stderr kept per request; more is dropped
	maxParamBytes  = 8 << 20  // all parameters of one request
)

// Param is one CGI variable.
type Param struct{ Name, Value string }

// Request is one FastCGI request.
type Request struct {
	Params []Param
	// Stdin is the request body. It is read until EOF in another goroutine and
	// is not touched after Response.Close returns. Nil means no body.
	Stdin io.Reader
}

// Client talks to one FastCGI backend. It is safe for concurrent use.
type Client struct {
	Network string // "unix" or "tcp"
	Address string

	// Timeout is how long the backend may take to answer once the request has
	// been sent, and how long a single write to it may block. Zero means 60 s.
	Timeout time.Duration
	// DialTimeout limits connecting. Zero means 5 s.
	DialTimeout time.Duration
	// MaxIdle is how many finished connections are kept for reuse. Zero means
	// 8; negative disables reuse (each request uses its own connection).
	MaxIdle int
	// IdleTimeout is how long a kept connection may sit unused. Zero means 30 s.
	IdleTimeout time.Duration
	// MaxConns limits simultaneous connections to the backend; further requests
	// wait. Zero means no limit.
	MaxConns int

	once sync.Once
	sem  chan struct{}

	mu     sync.Mutex
	idle   []*pooled
	closed bool
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 60 * time.Second
}

func (c *Client) maxIdle() int {
	switch {
	case c.MaxIdle == 0:
		return 8
	case c.MaxIdle < 0:
		return 0
	}
	return c.MaxIdle
}

func (c *Client) idleTimeout() time.Duration {
	if c.IdleTimeout > 0 {
		return c.IdleTimeout
	}
	return 30 * time.Second
}

func (c *Client) init() {
	c.once.Do(func() {
		if c.MaxConns > 0 {
			c.sem = make(chan struct{}, c.MaxConns)
		}
	})
}

// Close closes the kept connections. Requests in progress are not affected,
// but their connections are not kept afterwards.
func (c *Client) Close() {
	c.mu.Lock()
	idle := c.idle
	c.idle, c.closed = nil, true
	c.mu.Unlock()
	for _, p := range idle {
		p.discard()
	}
}

// pooled is a connection plus its buffers. While idle, a goroutine watches it
// so a connection the backend closed is noticed and not handed out.
type pooled struct {
	c         net.Conn
	br        *bufio.Reader
	bw        *bufio.Writer
	idleSince time.Time

	done chan struct{} // closed when the idle watcher exits
	dead bool          // set by the watcher before it exits
}

func newPooled(c net.Conn) *pooled {
	return &pooled{c: c, br: bufio.NewReaderSize(c, 32<<10), bw: bufio.NewWriterSize(c, 32<<10)}
}

func (p *pooled) discard() { p.c.Close() }

// watch starts the idle watcher: a read that only returns when the backend
// sends something (it must not) or closes the connection.
func (p *pooled) watch() {
	p.done = make(chan struct{})
	p.dead = false
	go func() {
		defer close(p.done)
		var b [1]byte
		_, err := p.c.Read(b[:])
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return // stopped by unwatch
		}
		p.dead = true
	}()
}

// unwatch stops the watcher and reports whether the connection is still usable.
func (p *pooled) unwatch() bool {
	_ = p.c.SetReadDeadline(time.Unix(1, 0))
	<-p.done
	if p.dead {
		return false
	}
	return p.c.SetReadDeadline(time.Time{}) == nil
}

// get returns an idle connection that is still alive, or nil.
func (c *Client) get() *pooled {
	for {
		c.mu.Lock()
		n := len(c.idle)
		if n == 0 {
			c.mu.Unlock()
			return nil
		}
		p := c.idle[n-1] // most recently used first
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		if time.Since(p.idleSince) > c.idleTimeout() || !p.unwatch() {
			p.discard()
			continue
		}
		return p
	}
}

func (c *Client) put(p *pooled) {
	p.idleSince = time.Now()
	c.mu.Lock()
	if c.closed || len(c.idle) >= c.maxIdle() {
		c.mu.Unlock()
		p.discard()
		return
	}
	p.watch()
	c.idle = append(c.idle, p)
	c.mu.Unlock()
}

func (c *Client) dial(ctx context.Context) (*pooled, error) {
	d := net.Dialer{Timeout: c.DialTimeout}
	if d.Timeout == 0 {
		d.Timeout = 5 * time.Second
	}
	conn, err := d.DialContext(ctx, c.Network, c.Address)
	if err != nil {
		return nil, &DialError{Err: err}
	}
	return newPooled(conn), nil
}

func validate(params []Param) error {
	total := 0
	for _, p := range params {
		if p.Name == "" || strings.ContainsAny(p.Name, "\x00=") {
			return fmt.Errorf("%w: bad name %q", ErrBadParam, p.Name)
		}
		// PHP and the C code under it end strings at a NUL: a value with one
		// would be silently cut short, which has been used to bypass checks.
		if strings.IndexByte(p.Value, 0) >= 0 {
			return fmt.Errorf("%w: %s contains a NUL byte", ErrBadParam, p.Name)
		}
		total += len(p.Name) + len(p.Value) + 8
	}
	if total > maxParamBytes {
		return fmt.Errorf("%w: the parameters are too large", ErrBadParam)
	}
	return nil
}

// Response is the backend's answer. Read its Body to the end (or Close it)
// when done.
type Response struct {
	Status int         // from the Status header; 200 if none, 302 for Location alone
	Header http.Header // CGI headers except Status
	Body   io.ReadCloser

	// AppStatus is the exit status the backend reported. It is valid once Body
	// has been read to EOF.
	AppStatus uint32

	cl      *Client
	pc      *pooled
	st      *stream
	s       *sender
	rest    io.Reader
	stopCtx func() bool
	release func()
	ctx     context.Context

	closeOnce sync.Once
}

// Stderr returns what the backend wrote to stderr (the PHP error log, for
// php-cgi), at most 64 KiB. It is complete once Body has been read or closed.
func (r *Response) Stderr() string { return r.st.stderr.String() }

// sender is the goroutine that writes the request while the response is read.
type sender struct {
	done chan struct{}

	mu          sync.Mutex
	headersDone bool
	bodyTouched bool  // the request body has been read from
	bodyErr     error // reading the request body failed
	connErr     error // writing to the backend failed
}

// touchedBody reports whether the request body has been read from (so it can't
// be sent again). Valid once the sender has finished.
func (s *sender) touchedBody() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodyTouched
}

func (s *sender) connFailed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connErr != nil
}

func (s *sender) failure() (body, conn error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodyErr, s.connErr
}

// Do sends the request and returns once the backend's response headers have
// arrived. The request body is sent concurrently, so the backend may answer
// before it has read all of it.
//
// ctx covers the whole exchange including the body: cancelling it closes the
// connection.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	if err := validate(req.Params); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.init()
	release := func() {}
	if c.sem != nil {
		select {
		case c.sem <- struct{}{}:
			release = func() { <-c.sem }
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	pc := c.get()
	reused := pc != nil
	for {
		if pc == nil {
			var err error
			if pc, err = c.dial(ctx); err != nil {
				release()
				return nil, err
			}
		}

		r := &Response{cl: c, pc: pc, ctx: ctx, release: release}
		r.st = &stream{br: pc.br}
		r.s = &sender{done: make(chan struct{})}
		r.stopCtx = context.AfterFunc(ctx, func() { pc.c.Close() })
		keep := c.maxIdle() > 0
		go r.send(req, keep, c.timeout())

		err := r.readHeaders()
		if err == nil {
			r.Body = &body{r: r}
			return r, nil
		}
		// Taken before abort closes the connection, which would make the sender's
		// write fail too and look like a broken connection.
		writeFailed := r.s.connFailed()
		err = r.explain(err)
		if msg := r.st.stderr.String(); msg != "" {
			err = &StderrError{Err: err, Stderr: msg}
		}
		r.abort(true) // the slot is kept for the retry, and given up below if there is none
		// A connection that sat in the pool can have been closed by the backend
		// (PHP-FPM does after pm.max_requests) in the instant before it was
		// used, too soon for the idle watcher to have seen it. When writing the
		// request to it fails and nothing came back, the request can't have been
		// run (the backend runs a script only once it has all of it): send it
		// again on a connection that is new. A connection that breaks while
		// waiting for the answer is not retried: the script may have run, or
		// have killed its worker, and running it twice is worse than an error.
		if reused && ctx.Err() == nil && r.st.records == 0 && !r.s.touchedBody() && writeFailed {
			reused, pc = false, nil
			continue
		}
		release()
		return nil, err
	}
}

// explain turns a read failure into the most useful error: a failing request
// body or a cancelled context is the cause of the connection errors that follow.
func (r *Response) explain(err error) error {
	bodyErr, connErr := r.s.failure()
	switch {
	case bodyErr != nil:
		return &BodyError{Err: bodyErr}
	case r.ctx.Err() != nil:
		return r.ctx.Err()
	case errors.Is(err, ErrNoResponse), errors.Is(err, ErrMalformedResponse), errors.Is(err, ErrOverloaded), errors.Is(err, errProtocol):
		return err
	case connErr != nil && !errors.Is(err, io.EOF):
		return fmt.Errorf("fastcgi: sending the request: %w", connErr)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("fastcgi: the backend did not answer in time: %w", err)
	}
	return err
}

// send writes the request. It runs in its own goroutine.
func (r *Response) send(req *Request, keep bool, timeout time.Duration) {
	s, pc := r.s, r.pc
	defer close(s.done)

	fail := func(body bool, err error) {
		s.mu.Lock()
		if body {
			s.bodyErr = err
		} else if s.connErr == nil {
			s.connErr = err
		}
		s.mu.Unlock()
		pc.c.Close() // unblocks the reader
	}
	// armWrite bounds how long the backend may stall us.
	armWrite := func() { _ = pc.c.SetWriteDeadline(time.Now().Add(timeout)) }

	var begin [8]byte
	binary.BigEndian.PutUint16(begin[:2], roleResponder)
	if keep {
		begin[2] = flagKeepConn
	}
	armWrite()
	bw := pc.bw
	err := writeRecord(bw, typeBeginRequest, begin[:])
	if err == nil {
		err = writeStream(bw, typeParams, encodeParams(req.Params))
	}
	if err == nil {
		err = writeRecord(bw, typeParams, nil)
	}
	if err == nil && req.Stdin == nil {
		err = writeRecord(bw, typeStdin, nil)
	}
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		fail(false, err)
		return
	}

	if req.Stdin != nil {
		buf := make([]byte, 32<<10)
		s.mu.Lock()
		s.bodyTouched = true
		s.mu.Unlock()
		for {
			n, rerr := req.Stdin.Read(buf)
			if n > 0 {
				armWrite()
				if err := writeStream(bw, typeStdin, buf[:n]); err != nil {
					fail(false, err)
					return
				}
				if err := bw.Flush(); err != nil {
					fail(false, err)
					return
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				fail(true, rerr)
				return
			}
		}
		armWrite()
		if err := writeRecord(bw, typeStdin, nil); err != nil {
			fail(false, err)
			return
		}
		if err := bw.Flush(); err != nil {
			fail(false, err)
			return
		}
	}

	// The request is out: now the backend's time to answer starts. (PHP-FPM
	// reads the whole body before running the script, so a slow upload must not
	// count against it.)
	s.mu.Lock()
	if !s.headersDone {
		_ = pc.c.SetReadDeadline(time.Now().Add(timeout))
	}
	s.mu.Unlock()
}

// readHeaders reads the CGI headers from the backend's stdout.
func (r *Response) readHeaders() error {
	lr := &io.LimitedReader{R: r.st, N: maxHeaderBytes}
	br := bufio.NewReaderSize(lr, 4096)
	mh, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		switch {
		case r.st.err != nil && r.st.err != io.EOF:
			err = r.st.err
		case lr.N <= 0:
			err = fmt.Errorf("%w: the headers are larger than %d bytes", ErrMalformedResponse, maxHeaderBytes)
		case !r.st.sawStdout:
			err = ErrNoResponse
		default:
			err = fmt.Errorf("%w: %v", ErrMalformedResponse, err)
		}
		return err
	}

	status := 0
	if v := mh.Get("Status"); v != "" {
		code, _, _ := strings.Cut(strings.TrimSpace(v), " ")
		n, err := strconv.Atoi(code)
		if err != nil || n < 100 || n > 599 {
			return fmt.Errorf("%w: bad Status %q", ErrMalformedResponse, v)
		}
		status = n
		mh.Del("Status")
	}
	if status == 0 {
		status = http.StatusOK
		if mh.Get("Location") != "" {
			status = http.StatusFound
		}
	}
	r.Status, r.Header = status, http.Header(mh)

	// Whatever the header reader buffered beyond the blank line is body.
	buffered, _ := br.Peek(br.Buffered())
	r.rest = io.MultiReader(bytes.NewReader(append([]byte(nil), buffered...)), r.st)

	// Headers are in: the response deadline no longer applies.
	r.s.mu.Lock()
	r.s.headersDone = true
	_ = r.pc.c.SetReadDeadline(time.Time{})
	r.s.mu.Unlock()
	return nil
}

// abort ends a request that failed before a response existed. It gives up the
// concurrency slot unless keepSlot is set (to try again).
func (r *Response) abort(keepSlot bool) {
	r.pc.c.Close()
	r.stopCtx()
	<-r.s.done
	if !keepSlot {
		r.release()
	}
}

type body struct{ r *Response }

func (b *body) Read(p []byte) (int, error) {
	n, err := b.r.rest.Read(p)
	if err == io.EOF {
		b.r.AppStatus = b.r.st.appStatus
		return n, io.EOF
	}
	if err != nil {
		return n, b.r.explain(err)
	}
	return n, nil
}

func (b *body) Close() error {
	r := b.r
	r.closeOnce.Do(func() {
		reusable := true
		// A sender that has written everything is only a moment from returning
		// (the backend can answer before the goroutine is scheduled again), so
		// give it that moment. One that is still going after it is blocked: the
		// backend answered without reading the whole request. That connection
		// can't be reused, and closing it is what stops the sender.
		grace := time.NewTimer(senderGrace)
		select {
		case <-r.s.done:
			grace.Stop()
		case <-grace.C:
			reusable = false
			r.pc.c.Close()
		}
		// If the context fired, its callback already closed the connection.
		if !r.stopCtx() {
			reusable = false
		}
		<-r.s.done
		bodyErr, connErr := r.s.failure()
		if !r.st.complete || bodyErr != nil || connErr != nil {
			reusable = false
		}
		if reusable {
			r.cl.put(r.pc)
		} else {
			r.pc.c.Close()
		}
		r.release()
	})
	return nil
}

// stream reads the STDOUT records of a response as a byte stream, collecting
// STDERR and noticing the end of the request.
type stream struct {
	br  *bufio.Reader
	buf []byte
	cur []byte

	err        error // terminal: io.EOF after a clean end, else why it failed
	stdoutDone bool
	sawStdout  bool
	records    int  // records read from the backend
	complete   bool // FCGI_END_REQUEST with REQUEST_COMPLETE was received
	appStatus  uint32
	stderr     cappedBuffer
}

func (s *stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(s.cur) > 0 {
			n := copy(p, s.cur)
			s.cur = s.cur[n:]
			return n, nil
		}
		if s.err != nil {
			return 0, s.err
		}
		typ, id, content, err := readRecord(s.br, &s.buf)
		if err != nil {
			var ne net.Error
			switch {
			case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
				err = ErrBackendClosed
			case errors.Is(err, errProtocol):
			case errors.As(err, &ne) && ne.Timeout():
			default:
				// A reset or similar: the backend went away.
				err = fmt.Errorf("%w (%v)", ErrBackendClosed, err)
			}
			s.err = err
			return 0, err
		}
		s.records++
		if id != requestID && id != 0 {
			s.err = protocolError("record for request %d, but only request %d is in progress", id, requestID)
			return 0, s.err
		}
		if id == 0 {
			continue // management records (GET_VALUES_RESULT, UNKNOWN_TYPE): not for us
		}
		switch typ {
		case typeStdout:
			if len(content) == 0 {
				s.stdoutDone = true
				continue
			}
			if s.stdoutDone {
				s.err = protocolError("stdout data after the end of stdout")
				return 0, s.err
			}
			s.sawStdout = true
			s.cur = content
		case typeStderr:
			s.stderr.Write(content)
		case typeEndRequest:
			if len(content) < 8 {
				s.err = protocolError("short END_REQUEST record")
				return 0, s.err
			}
			s.appStatus = binary.BigEndian.Uint32(content[:4])
			switch content[4] {
			case statusComplete:
				s.complete = true
				s.err = io.EOF
			case statusOverloaded, statusCantMPX:
				s.err = ErrOverloaded
			case statusUnknownRol:
				s.err = protocolError("the backend does not support the responder role")
			default:
				s.err = protocolError("unknown protocol status %d", content[4])
			}
			return 0, s.err
		default:
			// Unknown types are ignored.
		}
	}
}

// cappedBuffer keeps the first maxStderrBytes written to it.
type cappedBuffer struct {
	b         []byte
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) {
	room := maxStderrBytes - len(c.b)
	if len(p) > room {
		p, c.truncated = p[:max(room, 0)], true
	}
	c.b = append(c.b, p...)
}

func (c *cappedBuffer) String() string {
	if c.truncated {
		return string(c.b) + "\n[stderr truncated]"
	}
	return string(c.b)
}
