package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend records what the server asks of it.
type fakeBackend struct {
	mu       sync.Mutex
	reloaded []string
	stopped  []stopCall
	reopened int
	reloadFn func(path string) (ReloadResult, *Error)
}

type stopCall struct {
	graceful bool
	timeout  time.Duration
}

func (f *fakeBackend) Status() Status {
	return Status{Version: "9.9.9", PID: 42, Generation: 3, ConfigFile: "/etc/lotus/main.lotus", Sites: 2,
		Listeners: []Listener{{Name: "http", Address: "127.0.0.1:80"}}}
}
func (f *fakeBackend) Health() Health { return Health{Status: "ok", Checks: []HealthCheck{}} }
func (f *fakeBackend) Sites() []Site {
	return []Site{{Name: "a.test", Aliases: []string{"www.a.test"}, Listeners: []string{"http"}, TLS: "off"}}
}
func (f *fakeBackend) Reload(path string) (ReloadResult, *Error) {
	f.mu.Lock()
	f.reloaded = append(f.reloaded, path)
	fn := f.reloadFn
	f.mu.Unlock()
	if fn != nil {
		return fn(path)
	}
	return ReloadResult{Generation: 4, Diagnostics: []json.RawMessage{}}, nil
}
func (f *fakeBackend) Stop(graceful bool, timeout time.Duration) {
	f.mu.Lock()
	f.stopped = append(f.stopped, stopCall{graceful, timeout})
	f.mu.Unlock()
}
func (f *fakeBackend) ReopenLogs() (int, *Error) {
	f.mu.Lock()
	f.reopened++
	f.mu.Unlock()
	return 2, nil
}
func (f *fakeBackend) InspectConfig(site string) (json.RawMessage, *Error) {
	if site == "missing" {
		return nil, NewError(CodeNotFound, "there is no site named missing", nil)
	}
	return json.RawMessage(`{"config_version":1}`), nil
}

// rawConn is a test connection that speaks raw lines.
type rawConn struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func newPipe(t *testing.T, opts Options) (*rawConn, *Server, *fakeBackend) {
	t.Helper()
	fb := &fakeBackend{}
	if opts.Backend == nil {
		opts.Backend = fb
	} else {
		fb, _ = opts.Backend.(*fakeBackend)
	}
	opts.ServerName, opts.PID = "lotus/test", 4242
	s := NewServer(opts)
	cli, srv := net.Pipe()
	done := make(chan struct{})
	go func() { s.ServeConn(srv); close(done) }()
	t.Cleanup(func() { cli.Close(); <-done })
	return &rawConn{t: t, conn: cli, r: bufio.NewReader(cli)}, s, fb
}

func (c *rawConn) send(line string) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write([]byte(line + "\n")); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

type reply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

func (c *rawConn) recv() reply {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		c.t.Fatalf("bad reply %q: %v", line, err)
	}
	return r
}

func (c *rawConn) call(line string) reply { c.t.Helper(); c.send(line); return c.recv() }

const helloLine = `{"jsonrpc":"2.0","id":1,"method":"lotus.hello","params":{"protocol":"lotus.control.v1","client":"test"}}`

func hello(c *rawConn) { c.t.Helper(); c.call(helloLine) }

func TestHandshake(t *testing.T) {
	c, _, _ := newPipe(t, Options{})
	r := c.call(helloLine)
	if r.Error != nil {
		t.Fatalf("hello: %+v", r.Error)
	}
	var res HelloResult
	_ = json.Unmarshal(r.Result, &res)
	if res.Protocol != Protocol || res.PID != 4242 || res.Server != "lotus/test" || res.Role != "operator" || len(res.Methods) == 0 {
		t.Errorf("%+v", res)
	}
	if string(r.ID) != "1" {
		t.Errorf("id = %s", r.ID)
	}
}

func TestFirstCallMustBeHello(t *testing.T) {
	c, _, _ := newPipe(t, Options{})
	r := c.call(`{"jsonrpc":"2.0","id":7,"method":"lotus.status"}`)
	if r.Error == nil || r.Error.Code != CodeInvalidRequest || !strings.Contains(r.Error.Message, "lotus.hello") {
		t.Errorf("%+v", r.Error)
	}
	if string(r.ID) != "7" {
		t.Errorf("the error must carry the request id: %s", r.ID)
	}
}

func TestUnsupportedProtocol(t *testing.T) {
	c, _, _ := newPipe(t, Options{})
	r := c.call(`{"jsonrpc":"2.0","id":1,"method":"lotus.hello","params":{"protocol":"lotus.control.v9","client":"x"}}`)
	if r.Error == nil || r.Error.Code != CodeUnsupportedProto {
		t.Fatalf("%+v", r.Error)
	}
	if !strings.Contains(string(r.Error.Data), Protocol) {
		t.Errorf("the error must list supported protocols: %s", r.Error.Data)
	}
	// A failed hello does not unlock the connection.
	if r := c.call(`{"jsonrpc":"2.0","id":2,"method":"lotus.status"}`); r.Error == nil || r.Error.Code != CodeInvalidRequest {
		t.Errorf("%+v", r.Error)
	}
}

func TestMethods(t *testing.T) {
	c, _, fb := newPipe(t, Options{})
	hello(c)

	var st Status
	r := c.call(`{"jsonrpc":"2.0","id":2,"method":"lotus.status"}`)
	_ = json.Unmarshal(r.Result, &st)
	if r.Error != nil || st.PID != 42 || st.Generation != 3 || st.Sites != 2 {
		t.Errorf("status: %+v %+v", r.Error, st)
	}
	if r := c.call(`{"jsonrpc":"2.0","id":3,"method":"lotus.health","params":{}}`); r.Error != nil || !strings.Contains(string(r.Result), `"ok"`) {
		t.Errorf("health: %+v %s", r.Error, r.Result)
	}
	if r := c.call(`{"jsonrpc":"2.0","id":4,"method":"lotus.site.list"}`); r.Error != nil || !strings.Contains(string(r.Result), "a.test") {
		t.Errorf("site.list: %+v %s", r.Error, r.Result)
	}
	if r := c.call(`{"jsonrpc":"2.0","id":5,"method":"lotus.config.inspect","params":{"site":"a.test"}}`); r.Error != nil || !strings.Contains(string(r.Result), "config_version") {
		t.Errorf("inspect: %+v %s", r.Error, r.Result)
	}
	if r := c.call(`{"jsonrpc":"2.0","id":6,"method":"lotus.config.inspect","params":{"site":"missing"}}`); r.Error == nil || r.Error.Code != CodeNotFound {
		t.Errorf("inspect missing: %+v", r.Error)
	}
	if r := c.call(`{"jsonrpc":"2.0","id":7,"method":"lotus.logs.reopen"}`); r.Error != nil || !strings.Contains(string(r.Result), `"reopened":2`) {
		t.Errorf("logs.reopen: %+v %s", r.Error, r.Result)
	}
	r = c.call(`{"jsonrpc":"2.0","id":8,"method":"lotus.reload","params":{"config":"/etc/other.lotus"}}`)
	if r.Error != nil || !strings.Contains(string(r.Result), `"generation":4`) {
		t.Errorf("reload: %+v %s", r.Error, r.Result)
	}
	if len(fb.reloaded) != 1 || fb.reloaded[0] != "/etc/other.lotus" {
		t.Errorf("reload path = %v", fb.reloaded)
	}
	c.call(`{"jsonrpc":"2.0","id":9,"method":"lotus.reload"}`)
	if len(fb.reloaded) != 2 || fb.reloaded[1] != "" {
		t.Errorf("a reload without a path uses the current file: %v", fb.reloaded)
	}
}

func TestStop(t *testing.T) {
	c, _, fb := newPipe(t, Options{})
	hello(c)
	if r := c.call(`{"jsonrpc":"2.0","id":2,"method":"lotus.stop"}`); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := c.call(`{"jsonrpc":"2.0","id":3,"method":"lotus.stop","params":{"graceful":false,"timeout":"5s"}}`); r.Error != nil {
		t.Fatal(r.Error)
	}
	want := []stopCall{{true, 30 * time.Second}, {false, 5 * time.Second}}
	if len(fb.stopped) != 2 || fb.stopped[0] != want[0] || fb.stopped[1] != want[1] {
		t.Errorf("stop calls = %+v, want %+v", fb.stopped, want)
	}
	for _, bad := range []string{`"timeout":"soon"`, `"timeout":"-5s"`} {
		r := c.call(`{"jsonrpc":"2.0","id":4,"method":"lotus.stop","params":{` + bad + `}}`)
		if r.Error == nil || r.Error.Code != CodeInvalidParams {
			t.Errorf("%s: %+v", bad, r.Error)
		}
	}
	if len(fb.stopped) != 2 {
		t.Error("an invalid stop request must not stop the server")
	}
}

func TestReloadErrorsKeepTheirCodeAndData(t *testing.T) {
	fb := &fakeBackend{reloadFn: func(string) (ReloadResult, *Error) {
		return ReloadResult{}, NewError(CodeConfigInvalid, "invalid", map[string]any{"diagnostics": []string{"x"}})
	}}
	c, _, _ := newPipe(t, Options{Backend: fb})
	hello(c)
	r := c.call(`{"jsonrpc":"2.0","id":2,"method":"lotus.reload"}`)
	if r.Error == nil || r.Error.Code != CodeConfigInvalid || !strings.Contains(string(r.Error.Data), "diagnostics") {
		t.Errorf("%+v", r.Error)
	}
}

func TestInvalidRequests(t *testing.T) {
	c, _, _ := newPipe(t, Options{})
	hello(c)
	tests := []struct {
		name, line string
		code       int
	}{
		{"not JSON", `{{{`, CodeParse},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"lotus.status"}`, CodeInvalidRequest},
		{"no method", `{"jsonrpc":"2.0","id":1}`, CodeInvalidRequest},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"lotus.status"}]`, CodeParse},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"lotus.nope"}`, CodeMethodNotFound},
		{"unknown field", `{"jsonrpc":"2.0","id":1,"method":"lotus.reload","params":{"confg":"x"}}`, CodeInvalidParams},
		{"wrong type", `{"jsonrpc":"2.0","id":1,"method":"lotus.reload","params":{"config":5}}`, CodeInvalidParams},
		{"params where none are allowed", `{"jsonrpc":"2.0","id":1,"method":"lotus.status","params":{"x":1}}`, CodeInvalidParams},
		{"positional params", `{"jsonrpc":"2.0","id":1,"method":"lotus.reload","params":["x"]}`, CodeInvalidParams},
	}
	for _, tt := range tests {
		r := c.call(tt.line)
		if r.Error == nil || r.Error.Code != tt.code {
			t.Errorf("%s: %+v, want code %d", tt.name, r.Error, tt.code)
		}
	}
	// The connection is still usable after errors.
	if r := c.call(`{"jsonrpc":"2.0","id":9,"method":"lotus.health"}`); r.Error != nil {
		t.Errorf("%+v", r.Error)
	}
}

func TestStringIDsAndNotifications(t *testing.T) {
	c, _, fb := newPipe(t, Options{})
	hello(c)
	r := c.call(`{"jsonrpc":"2.0","id":"abc","method":"lotus.health"}`)
	if string(r.ID) != `"abc"` {
		t.Errorf("id = %s", r.ID)
	}
	// A notification (no id) is carried out but not answered: the next reply
	// belongs to the next request.
	c.send(`{"jsonrpc":"2.0","method":"lotus.logs.reopen"}`)
	r = c.call(`{"jsonrpc":"2.0","id":11,"method":"lotus.health"}`)
	if string(r.ID) != "11" {
		t.Errorf("the notification was answered: %s", r.ID)
	}
	if fb.reopened != 1 {
		t.Errorf("the notification was not carried out: %d", fb.reopened)
	}
}

func TestRequestsOnOneConnectionAreHandledInOrder(t *testing.T) {
	c, _, _ := newPipe(t, Options{})
	hello(c)
	// net.Pipe has no buffer, so the requests are written while replies are read.
	go func() {
		for i := 2; i < 20; i++ {
			_, _ = c.conn.Write([]byte(`{"jsonrpc":"2.0","id":` + itoa(i) + `,"method":"lotus.health"}` + "\n"))
		}
	}()
	for i := 2; i < 20; i++ {
		if r := c.recv(); string(r.ID) != itoa(i) {
			t.Fatalf("reply %d has id %s", i, r.ID)
		}
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestOversizedMessageClosesTheConnection(t *testing.T) {
	c, _, _ := newPipe(t, Options{})
	hello(c)
	big := `{"jsonrpc":"2.0","id":1,"method":"lotus.health","params":{"x":"` + strings.Repeat("a", MaxMessageSize) + `"}}`
	go func() { _, _ = c.conn.Write([]byte(big + "\n")) }() // the server may close before reading it all
	r := c.recv()
	if r.Error == nil || r.Error.Code != CodeInvalidRequest {
		t.Errorf("%+v", r.Error)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.r.ReadByte(); err == nil {
		t.Error("the connection must be closed after an oversized message")
	}
}

func TestIdleConnectionsAreClosed(t *testing.T) {
	c, _, _ := newPipe(t, Options{IdleTimeout: 80 * time.Millisecond})
	hello(c)
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.r.ReadByte(); !errors.Is(err, io.EOF) && err == nil {
		t.Errorf("expected the idle connection to close, err = %v", err)
	}
}

func TestOperatorCallsAreAudited(t *testing.T) {
	var logs bytes.Buffer
	c, _, _ := newPipe(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	hello(c)
	c.call(`{"jsonrpc":"2.0","id":2,"method":"lotus.status"}`)
	c.call(`{"jsonrpc":"2.0","id":3,"method":"lotus.reload"}`)
	c.call(`{"jsonrpc":"2.0","id":4,"method":"lotus.stop"}`)
	out := logs.String()
	if strings.Count(out, "audit=true") != 2 || !strings.Contains(out, "method=lotus.reload") || !strings.Contains(out, "method=lotus.stop") {
		t.Errorf("operator calls must be audited, read-only ones not:\n%s", out)
	}
	if strings.Contains(out, "method=lotus.status") {
		t.Errorf("status is not an operator call:\n%s", out)
	}
}

func TestAtMostSixteenConnections(t *testing.T) {
	s := NewServer(Options{Backend: &fakeBackend{}, ServerName: "t", PID: 1})
	ln := newChanListener()
	go s.Serve(ln)
	defer s.Close()

	var held []net.Conn
	for i := 0; i < MaxConnections; i++ {
		c := ln.dial(t)
		held = append(held, c)
		if _, err := NewClient(c, "t"); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
	}
	extra := ln.dial(t)
	_ = extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); err == nil {
		t.Error("the 17th connection must be refused at accept time")
	}
	// Freeing one makes room.
	held[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c := ln.dial(t)
		if _, err := NewClient(c, "t"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a freed slot was never reused")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, c := range held {
		c.Close()
	}
}

// chanListener is an in-memory net.Listener.
type chanListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}
func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *chanListener) Addr() net.Addr { return pipeAddr{} }
func (l *chanListener) dial(t *testing.T) net.Conn {
	t.Helper()
	cli, srv := net.Pipe()
	select {
	case l.conns <- srv:
	case <-time.After(3 * time.Second):
		t.Fatal("the server did not accept")
	}
	return cli
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestClientCallsAndErrors(t *testing.T) {
	fb := &fakeBackend{}
	s := NewServer(Options{Backend: fb, ServerName: "lotus/test", PID: 7})
	cli, srv := net.Pipe()
	go s.ServeConn(srv)
	c, err := NewClient(cli, "unit-test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Hello.PID != 7 {
		t.Errorf("hello: %+v", c.Hello)
	}
	var st Status
	if err := c.Call(MethodStatus, nil, &st); err != nil || st.Generation != 3 {
		t.Errorf("%v %+v", err, st)
	}
	err = c.Call(MethodConfigInspect, InspectParams{Site: "missing"}, nil)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != CodeNotFound {
		t.Errorf("err = %v", err)
	}
}

// The framing and JSON decoding must survive any input: no panic, no hang.
func FuzzServeConn(f *testing.F) {
	f.Add([]byte(helloLine + "\n" + `{"jsonrpc":"2.0","id":2,"method":"lotus.stop","params":{"timeout":"1s"}}` + "\n"))
	f.Add([]byte("\n\n[]\n{\"jsonrpc\":\"2.0\"}\n\x00\xff"))
	f.Add([]byte(`{"jsonrpc":"2.0","id":{"a":1},"method":"lotus.hello","params":null}` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s := NewServer(Options{Backend: &fakeBackend{}, ServerName: "t", PID: 1, IdleTimeout: 200 * time.Millisecond})
		cli, srv := net.Pipe()
		done := make(chan struct{})
		go func() { s.ServeConn(srv); close(done) }()
		go func() { _, _ = io.Copy(io.Discard, cli) }() // drain replies
		_, _ = cli.Write(data)
		cli.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("ServeConn did not return")
		}
	})
}

// Closing the server must not cut off a request that is being answered: this
// is what lets `lotus stop` receive its acknowledgement.
func TestCloseLetsARunningRequestFinish(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	fb := &fakeBackend{reloadFn: func(string) (ReloadResult, *Error) {
		close(started)
		<-release
		return ReloadResult{Generation: 9, Diagnostics: []json.RawMessage{}}, nil
	}}
	s := NewServer(Options{Backend: fb, ServerName: "t", PID: 1})
	ln := newChanListener()
	go s.Serve(ln)

	conn := ln.dial(t)
	c, err := NewClient(conn, "t")
	if err != nil {
		t.Fatal(err)
	}
	idle := ln.dial(t) // a second connection that is only waiting
	if _, err := NewClient(idle, "t"); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	var res ReloadResult
	go func() { result <- c.Call(MethodReload, nil, &res) }()
	<-started

	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	// The idle connection is closed at once...
	_ = idle.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := idle.Read(make([]byte, 1)); err == nil {
		t.Error("an idle connection must be closed by Close")
	}
	// ...but Close waits for the running request.
	select {
	case <-closed:
		t.Fatal("Close returned while a request was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil || res.Generation != 9 {
		t.Errorf("the running request must still get its reply: %v %+v", err, res)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return after the request finished")
	}
}
