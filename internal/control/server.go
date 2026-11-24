package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Backend is what the control channel operates on. The application
// implements it; every method may be called from several connections at once.
type Backend interface {
	Status() Status
	Health() Health
	Sites() []Site
	// Certificates lists the certificates in use and those still to be obtained.
	Certificates() []Certificate
	// Reload switches to the configuration in path (empty: the file the server
	// started with) and returns when it is published or rejected. A rejected
	// configuration is an *Error with CodeConfigInvalid (diagnostics in Data),
	// CodeReloadInProgress, or CodeInternal.
	Reload(path string) (ReloadResult, *Error)
	// Stop asks the server to shut down. It returns at once.
	Stop(graceful bool, timeout time.Duration)
	// ReopenLogs reopens the log files and reports how many it opened.
	ReopenLogs() (int, *Error)
	// InspectConfig returns the running configuration as JSON, secrets redacted.
	InspectConfig(site string) (json.RawMessage, *Error)
}

// Options configure a Server.
type Options struct {
	Backend Backend
	// ServerName is reported in the handshake, for example "lotus/0.1.0".
	ServerName string
	PID        int
	Logger     *slog.Logger
	// IdleTimeout overrides the default; tests use it.
	IdleTimeout time.Duration
}

// Server serves the control protocol on a listener.
type Server struct {
	opts  Options
	log   *slog.Logger
	slots chan struct{}

	mu     sync.Mutex
	ln     net.Listener
	conns  map[*connState]struct{}
	closed bool
	wg     sync.WaitGroup
}

// connState lets Close tell a connection that is waiting for a request (it can
// be closed at once) from one that is running a request (it must be allowed to
// write its reply first).
type connState struct {
	conn net.Conn
	busy bool
}

// NewServer returns a Server. Call Serve with a listener.
func NewServer(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = IdleTimeout
	}
	return &Server{opts: opts, log: opts.Logger, slots: make(chan struct{}, MaxConnections), conns: map[*connState]struct{}{}}
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		select {
		case s.slots <- struct{}{}:
		default:
			conn.Close() // too many connections: refuse at accept time
			s.log.Warn("control connection refused: too many connections")
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			s.ServeConn(conn)
		}()
	}
}

// closeGrace is how long Close waits for requests in progress before it
// drops their connections.
const closeGrace = 5 * time.Second

// Close stops accepting connections and closes the open ones. A connection that
// is in the middle of a request is allowed to write its reply first (so
// `lotus stop` gets its answer), for up to closeGrace.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	for st := range s.conns {
		if !st.busy {
			st.conn.Close()
		}
	}
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(closeGrace):
		s.mu.Lock()
		for st := range s.conns {
			st.conn.Close()
		}
		s.mu.Unlock()
		<-done
	}
	return err
}

// ServeConn serves one connection until it closes. It is exported so tests can
// use an in-memory pipe.
func (s *Server) ServeConn(conn net.Conn) {
	defer conn.Close()
	st := &connState{conn: conn}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.conns[st] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, st)
		s.mu.Unlock()
	}()
	peer := peerOf(conn)
	r := bufio.NewReaderSize(conn, 4096)
	enc := func(resp response) error {
		b, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err = conn.Write(append(b, '\n'))
		return err
	}
	hello := false
	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.opts.IdleTimeout))
		line, err := readLine(r, MaxMessageSize)
		if errors.Is(err, errTooLong) {
			_ = enc(response{JSONRPC: jsonRPCVersion, ID: json.RawMessage("null"), Error: NewError(CodeInvalidRequest, "message too large", nil)})
			return
		}
		if err != nil {
			return // closed, idle timeout or reset
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		s.setBusy(st, true)
		resp, send := s.handle(line, &hello, peer)
		var werr error
		if send {
			werr = enc(resp)
		}
		if closing := s.setBusy(st, false); werr != nil || closing {
			return // a reply was undeliverable, or the server is closing and this one is done
		}
	}
}

// setBusy marks the connection as running (or done with) a request. It reports
// whether the server is closing.
func (s *Server) setBusy(st *connState, busy bool) (closing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.busy = busy
	return s.closed
}

var errTooLong = errors.New("message too long")

// readLine reads one line of at most max bytes (not counting the newline).
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max+1 {
			return nil, errTooLong
		}
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// handle runs one request. send is false for notifications.
func (s *Server) handle(line []byte, hello *bool, peer peerInfo) (response, bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return fail(json.RawMessage("null"), CodeParse, "parse error"), true
	}
	id := req.ID
	notification := len(id) == 0
	if notification {
		id = json.RawMessage("null")
	}
	if req.JSONRPC != jsonRPCVersion || req.Method == "" {
		return fail(id, CodeInvalidRequest, "not a JSON-RPC 2.0 request"), true
	}

	var result any
	var rpcErr *Error
	if req.Method == MethodHello {
		result, rpcErr = s.hello(req.Params)
		if rpcErr == nil {
			*hello = true
		}
	} else if !*hello {
		rpcErr = NewError(CodeInvalidRequest, "the first call must be "+MethodHello, nil)
	} else {
		result, rpcErr = s.call(req.Method, req.Params, peer)
	}
	if notification {
		return response{}, false
	}
	if rpcErr != nil {
		return response{JSONRPC: jsonRPCVersion, ID: id, Error: rpcErr}, true
	}
	if result == nil {
		result = struct{}{}
	}
	return response{JSONRPC: jsonRPCVersion, ID: id, Result: result}, true
}

func fail(id json.RawMessage, code int, msg string) response {
	return response{JSONRPC: jsonRPCVersion, ID: id, Error: NewError(code, msg, nil)}
}

func (s *Server) methods() []string {
	return []string{MethodStatus, MethodHealth, MethodReload, MethodStop, MethodLogsReopen, MethodConfigInspect, MethodSiteList, MethodCertList}
}

func (s *Server) hello(params json.RawMessage) (any, *Error) {
	var p HelloParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Protocol != Protocol {
		return nil, NewError(CodeUnsupportedProto, "unsupported protocol "+p.Protocol,
			map[string][]string{"supported": {Protocol}})
	}
	return HelloResult{Protocol: Protocol, Server: s.opts.ServerName, PID: s.opts.PID, Role: roleOperator, Methods: s.methods()}, nil
}

// operatorMethods change something; they are audited.
var operatorMethods = map[string]bool{MethodReload: true, MethodStop: true, MethodLogsReopen: true}

func (s *Server) call(method string, params json.RawMessage, peer peerInfo) (result any, rpcErr *Error) {
	b := s.opts.Backend
	audited := operatorMethods[method]
	if audited {
		defer func() {
			outcome := "ok"
			if rpcErr != nil {
				outcome = rpcErr.Message
			}
			s.log.Info("control call", "audit", true, "method", method, "outcome", outcome,
				"peer_uid", peer.UID, "peer_pid", peer.PID)
		}()
	}

	switch method {
	case MethodStatus:
		if err := noParams(params); err != nil {
			return nil, err
		}
		return b.Status(), nil
	case MethodHealth:
		if err := noParams(params); err != nil {
			return nil, err
		}
		return b.Health(), nil
	case MethodSiteList:
		if err := noParams(params); err != nil {
			return nil, err
		}
		return b.Sites(), nil
	case MethodCertList:
		if err := noParams(params); err != nil {
			return nil, err
		}
		return b.Certificates(), nil
	case MethodReload:
		var p ReloadParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		res, err := b.Reload(p.Config)
		if err != nil {
			return nil, err
		}
		return res, nil
	case MethodStop:
		var p StopParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		graceful := p.Graceful == nil || *p.Graceful
		timeout := 30 * time.Second
		if p.Timeout != "" {
			d, err := time.ParseDuration(p.Timeout)
			if err != nil || d <= 0 {
				return nil, NewError(CodeInvalidParams, "timeout must be a duration such as 30s", nil)
			}
			timeout = d
		}
		b.Stop(graceful, timeout)
		return struct{}{}, nil
	case MethodLogsReopen:
		if err := noParams(params); err != nil {
			return nil, err
		}
		n, err := b.ReopenLogs()
		if err != nil {
			return nil, err
		}
		return map[string]int{"reopened": n}, nil
	case MethodConfigInspect:
		var p InspectParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		out, err := b.InspectConfig(p.Site)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, NewError(CodeMethodNotFound, "unknown method "+method, nil)
}

// decodeParams reads params into v. Absent params leave v at its zero value.
// Unknown fields are an error, so a misspelled option isn't silently ignored.
func decodeParams(raw json.RawMessage, v any) *Error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return NewError(CodeInvalidParams, "invalid parameters: "+err.Error(), nil)
	}
	if dec.More() {
		return NewError(CodeInvalidParams, "invalid parameters: trailing data", nil)
	}
	return nil
}

// noParams accepts absent, null, [] and {} parameters.
func noParams(raw json.RawMessage) *Error {
	t := bytes.TrimSpace(raw)
	switch string(t) {
	case "", "null", "[]", "{}":
		return nil
	}
	return NewError(CodeInvalidParams, "this method takes no parameters", nil)
}
