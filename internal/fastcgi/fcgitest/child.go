package fcgitest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ChildEnv, when set, makes a test binary act as a fake php-cgi: call
// RunChildIfRequested first thing in TestMain. A supervisor under test then
// starts the test binary itself as its "php-cgi", with the arguments real
// php-cgi gets (-b ADDRESS, and -c FILE when a php.ini is configured).
//
// The value picks the behaviour:
//
//	"1"      serve normally
//	"hang"   never listen (a process that does not become ready)
//	"exit"   exit at once with status 4 (a process that cannot start)
const ChildEnv = "LOTUS_FAKE_PHPCGI"

// ChildPHPRC is a value of PHPRC that also makes a test binary act as a fake
// php-cgi. A supervisor passes PHPRC on to its processes, so a test can set it
// in its own environment instead of reaching into the supervisor's settings.
const ChildPHPRC = "lotus-fake-phpcgi"

// RunChildIfRequested runs the fake php-cgi and never returns if ChildEnv is
// set; otherwise it returns at once.
//
// The fake answers every request with its own process id (the body) so tests
// can tell processes apart. SCRIPT_NAME chooses special behaviour:
//
//	/slow          answer after 300 ms
//	/die           exit in the middle of the request
//	/env           answer with the environment variable named by QUERY_STRING
//	/args          answer with the program's arguments
//
// A response header X-Concurrent reports how many requests the process was
// handling at once: real php-cgi does one at a time, so anything above 1 means
// the supervisor sent it two requests together.
//
// PHP_FCGI_MAX_REQUESTS=N makes the process exit after N requests, as php-cgi does.
func RunChildIfRequested() {
	mode := os.Getenv(ChildEnv)
	if mode == "" && os.Getenv("PHPRC") == ChildPHPRC {
		mode = "1"
	}
	if mode == "" {
		return
	}
	switch mode {
	case "hang":
		time.Sleep(time.Hour)
		os.Exit(0)
	case "exit":
		os.Exit(4)
	}
	addr := ""
	for i, a := range os.Args {
		if a == "-b" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}
	network := "tcp"
	if filepath.IsAbs(addr) {
		network = "unix"
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake php-cgi: cannot listen:", err)
		os.Exit(2)
	}
	max, _ := strconv.Atoi(os.Getenv("PHP_FCGI_MAX_REQUESTS"))
	var served, active atomic.Int32
	NewOn(childTB{}, ln, func(r *Req) {
		n := active.Add(1)
		defer active.Add(-1)
		body := strconv.Itoa(os.Getpid())
		switch r.Params["SCRIPT_NAME"] {
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		case "/die":
			os.Exit(3)
		case "/env":
			body = os.Getenv(r.Params["QUERY_STRING"])
		case "/args":
			body = strings.Join(os.Args[1:], " ")
		}
		r.Respond("Content-Type: text/plain\r\nX-Concurrent: "+strconv.Itoa(int(n)), body)
		if max > 0 && int(served.Add(1)) >= max {
			os.Exit(0)
		}
	})
	select {}
}

type childTB struct{}

func (childTB) Helper() {}
func (childTB) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fake php-cgi: "+format+"\n", args...)
}
func (childTB) Fatal(args ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"fake php-cgi:"}, args...)...)
	os.Exit(2)
}
func (childTB) Cleanup(func()) {}
