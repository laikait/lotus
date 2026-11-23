package server

import (
	"os"
	"testing"

	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

// The test binary doubles as a fake php-cgi for the tests of managed PHP
// backends (see fcgitest.RunChildIfRequested).
func TestMain(m *testing.M) {
	fcgitest.RunChildIfRequested()
	os.Exit(m.Run())
}
