package fastcgi

import (
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"testing"
	"time"
)

// tempSocketDir returns a directory with a path short enough for a Unix socket
// (macOS allows 104 bytes).
func tempSocketDir(t *testing.T) (string, error) {
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "fc")
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir, nil
}

// FuzzBackendOutput feeds arbitrary bytes to the client as the backend's
// answer. Whatever they are, the client must neither panic nor hang, and must
// not claim success with a nonsensical response.
func FuzzBackendOutput(f *testing.F) {
	rec := func(typ byte, s string) []byte {
		n := len(s)
		pad := (8 - n%8) % 8
		b := []byte{1, typ, 0, 1, byte(n >> 8), byte(n), byte(pad), 0}
		return append(append(b, s...), make([]byte, pad)...)
	}
	end := rec(3, "\x00\x00\x00\x00\x00\x00\x00\x00")
	good := append(append(rec(6, "Status: 200 OK\r\nX-A: b\r\n\r\nbody"), rec(6, "")...), end...)
	f.Add(good)
	f.Add(rec(7, "only stderr"))
	f.Add(append(rec(6, "no blank line"), end...))
	f.Add([]byte{1, 6, 0, 1, 0xff, 0xff, 0, 0})
	f.Add([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip(err)
		}
		defer ln.Close()
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 4096)
			c.Read(buf) // the request; contents don't matter here
			c.Write(data)
		}()

		c := &Client{Network: "tcp", Address: ln.Addr().String(), Timeout: time.Second, MaxIdle: -1}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := c.Do(ctx, &Request{Params: []Param{{"A", "b"}}})
		if err != nil {
			return
		}
		if resp.Status < 100 || resp.Status > 599 {
			t.Errorf("status %d", resp.Status)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	})
}
