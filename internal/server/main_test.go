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
	// The exec DNS provider test runs this binary as its program.
	if len(os.Args) == 2 && (os.Args[1] == "present" || os.Args[1] == "cleanup") && os.Getenv("LOTUS_ACME_NAME") != "" {
		dnsHelper()
	}
	os.Exit(m.Run())
}
