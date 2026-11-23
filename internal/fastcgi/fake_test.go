package fastcgi

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeBackend is a FastCGI server for tests. It decodes the protocol on its
// own (not with the client's code), so a mistake made symmetrically in both
// directions can't hide.
type fakeBackend struct {
	t       *testing.T
	ln      net.Listener
	handler func(*fakeReq)

	accepted  atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
	wg        sync.WaitGroup
}

func newFake(t *testing.T, handler func(*fakeReq)) *fakeBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{t: t, ln: ln, handler: handler}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				f.serve(c)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); f.wg.Wait() })
	return f
}

func (f *fakeBackend) client() *Client {
	return &Client{Network: "tcp", Address: f.ln.Addr().String()}
}

// fakeReq is one request as the backend sees it. The handler runs as soon as
// the parameters are complete; the body is read on demand.
type fakeReq struct {
	Params   map[string]string
	KeepConn bool
	Role     uint16

	c       net.Conn
	stdin   []byte
	stdinOK bool
	closed  bool
}

func readRaw(c net.Conn) (typ uint8, id uint16, content []byte, err error) {
	var h [8]byte
	if _, err = io.ReadFull(c, h[:]); err != nil {
		return
	}
	typ = h[1]
	id = binary.BigEndian.Uint16(h[2:4])
	n := int(binary.BigEndian.Uint16(h[4:6]))
	buf := make([]byte, n+int(h[6]))
	if _, err = io.ReadFull(c, buf); err != nil {
		return
	}
	return typ, id, buf[:n], nil
}

func parsePairs(b []byte) map[string]string {
	m := map[string]string{}
	length := func() int {
		if b[0]&0x80 == 0 {
			n := int(b[0])
			b = b[1:]
			return n
		}
		n := int(binary.BigEndian.Uint32(b[:4]) & 0x7fffffff)
		b = b[4:]
		return n
	}
	for len(b) > 0 {
		nl := length()
		vl := length()
		m[string(b[:nl])] = string(b[nl : nl+vl])
		b = b[nl+vl:]
	}
	return m
}

func (f *fakeBackend) serve(c net.Conn) {
	defer c.Close()
	for {
		// BEGIN_REQUEST
		typ, id, content, err := readRaw(c)
		if err != nil {
			return
		}
		if typ != 1 || id != 1 || len(content) != 8 {
			f.t.Errorf("fake: expected BEGIN_REQUEST for id 1, got type %d id %d len %d", typ, id, len(content))
			return
		}
		r := &fakeReq{c: c, KeepConn: content[2]&1 != 0, Role: binary.BigEndian.Uint16(content[:2])}
		var params []byte
		for {
			typ, id, content, err = readRaw(c)
			if err != nil {
				return
			}
			if typ != 4 || id != 1 {
				f.t.Errorf("fake: expected PARAMS, got type %d id %d", typ, id)
				return
			}
			if len(content) == 0 {
				break
			}
			params = append(params, content...)
		}
		r.Params = parsePairs(params)

		n := f.active.Add(1)
		for {
			m := f.maxActive.Load()
			if n <= m || f.maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		f.handler(r)
		f.active.Add(-1)
		if r.closed || !r.KeepConn || !r.stdinOK {
			return
		}
	}
}

// Stdin reads the request body (all STDIN records up to the empty one).
func (r *fakeReq) Stdin() []byte {
	if r.stdinOK {
		return r.stdin
	}
	for {
		typ, id, content, err := readRaw(r.c)
		if err != nil || typ != 5 || id != 1 {
			r.Close()
			return nil
		}
		if len(content) == 0 {
			r.stdinOK = true
			return r.stdin
		}
		r.stdin = append(r.stdin, content...)
	}
}

func (r *fakeReq) record(typ uint8, content []byte) {
	for first := true; first || len(content) > 0; first = false {
		n := len(content)
		if n > 60000 {
			n = 60000
		}
		pad := (8 - n%8) % 8
		h := []byte{1, typ, 0, 1, byte(n >> 8), byte(n), byte(pad), 0}
		out := append(append(h, content[:n]...), make([]byte, pad)...)
		if _, err := r.c.Write(out); err != nil {
			return
		}
		content = content[n:]
		if n == 0 {
			return
		}
	}
}

// Stdout sends data as STDOUT records (no terminator).
func (r *fakeReq) Stdout(s string) {
	if s != "" {
		r.record(6, []byte(s))
	}
}

// Stderr sends data as STDERR records.
func (r *fakeReq) Stderr(s string) {
	if s != "" {
		r.record(7, []byte(s))
	}
}

// End terminates stdout and ends the request.
func (r *fakeReq) End(appStatus uint32, protocolStatus uint8) {
	r.record(6, nil)
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body, appStatus)
	body[4] = protocolStatus
	r.record(3, body)
}

// Close drops the connection.
func (r *fakeReq) Close() {
	r.closed = true
	r.c.Close()
}

// Respond is the usual well-behaved answer: read the body, send headers and body.
func (r *fakeReq) Respond(headers, body string) {
	r.Stdin()
	if headers == "" {
		r.Stdout("\r\n" + body) // no headers: just the blank line
	} else {
		r.Stdout(headers + "\r\n\r\n" + body)
	}
	r.End(0, 0)
}

// Drain reads and discards whatever the client is still sending until the
// connection ends, as PHP-FPM does when a script ignores its body. Closing
// with unread data would make the OS reset the connection, which can destroy
// the response in flight.
func (r *fakeReq) Drain() {
	buf := make([]byte, 64<<10)
	for {
		if _, err := r.c.Read(buf); err != nil {
			return
		}
	}
}
