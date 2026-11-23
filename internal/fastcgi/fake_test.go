package fastcgi

import (
	"testing"

	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

type fakeReq = fcgitest.Req

type fakeBackend struct{ *fcgitest.Backend }

func newFake(t *testing.T, handler func(*fakeReq)) *fakeBackend {
	t.Helper()
	return &fakeBackend{fcgitest.New(t, handler)}
}

func (f *fakeBackend) client() *Client {
	return &Client{Network: "tcp", Address: f.Addr()}
}
