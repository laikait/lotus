// Package phpproc starts and supervises php-cgi processes and hands requests
// to them.
//
// php-cgi serves FastCGI but handles one request at a time per process, so a
// pool of N processes allows N simultaneous requests. This package runs the
// pool: it starts the processes, waits until each accepts connections, gives
// every request an idle process, restarts a process that dies (with a growing
// delay if it keeps dying), and stops them all when the pool is closed.
//
// It exists for systems without PHP-FPM, chiefly Windows, but works anywhere
// php-cgi does. On Unix the processes listen on Unix sockets in a private
// directory; on Windows they listen on 127.0.0.1, which any account on the
// machine can reach (see docs/architecture/php.md).
package phpproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laikait/lotus/internal/fastcgi"
)

// Spec says what to run.
type Spec struct {
	// Command is the php-cgi program: a path, or a name looked up in PATH.
	Command string
	// Processes is how many to run. Each handles one request at a time.
	Processes int
	// MaxRequests replaces a process by a fresh one after it has served that
	// many requests, which contains memory leaks. 0 means never.
	//
	// The pool does this itself, while the process is idle, rather than leaving
	// it to php-cgi's own PHP_FCGI_MAX_REQUESTS: a process that exits by itself
	// can do so just as a request reaches it, and that request would fail.
	MaxRequests int
	// PHPIni is a php.ini file for php-cgi -c. Empty means PHP's usual search.
	PHPIni string
	// Timeout bounds the wait for a free process and, once a process has the
	// request, the wait for its answer.
	Timeout time.Duration
	// ReadyTimeout is how long a process may take to start accepting
	// connections. Zero means 15 seconds.
	ReadyTimeout time.Duration
	// ExtraEnv is added to the processes' environment (for tests).
	ExtraEnv []string
}

// Key identifies a Spec: two pools with the same key are interchangeable.
func (s Spec) Key() string {
	return fmt.Sprintf("%q|%d|%d|%q|%s|%s|%q", s.Command, s.Processes, s.MaxRequests, s.PHPIni, s.Timeout, s.ReadyTimeout, s.ExtraEnv)
}

// ErrClosed is returned for requests made after the pool was closed.
var ErrClosed = errors.New("phpproc: the pool is closed")

// Timing of restarts. A process that was retired on purpose (it reached
// MaxRequests) is replaced at once; one that crashes is replaced after a delay that doubles
// each time, up to maxBackoff, and starts over once a process has lived long
// enough.
const (
	minBackoff = 100 * time.Millisecond
	maxBackoff = 10 * time.Second
	stableTime = 30 * time.Second
)

// Pool is a set of supervised php-cgi processes.
type Pool struct {
	spec Spec
	log  *slog.Logger
	dir  string // Unix socket directory; empty on Windows

	workers []*worker
	free    chan *worker // idle, live workers (see worker.inFree)
	closing chan struct{}

	wg        sync.WaitGroup
	closeOnce sync.Once
	restarts  atomic.Int64
	logLimit  lineLimiter
}

type worker struct {
	p  *Pool
	id int

	mu       sync.Mutex
	alive    bool // a process is running and accepts connections
	busy     bool // a request holds it
	inFree   bool // it has an entry in Pool.free
	served   int  // requests given to the current process
	retiring bool // the current process is being replaced on purpose
	recycle  bool // replace the process as soon as the request it serves is done (RecycleAll)
	client   *fastcgi.Client
	cmd      *exec.Cmd

	first chan error // the outcome of the first start, for Start
}

// Start runs the processes and returns when all of them accept connections.
// If one can't be started, nothing is left running and the error says why.
func Start(spec Spec, log *slog.Logger) (*Pool, error) {
	if spec.Processes < 1 {
		return nil, errors.New("phpproc: at least one process is needed")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 60 * time.Second
	}
	if spec.ReadyTimeout <= 0 {
		spec.ReadyTimeout = 15 * time.Second
	}
	path, err := exec.LookPath(spec.Command)
	if err != nil {
		return nil, fmt.Errorf("php-cgi program %q not found: %w", spec.Command, err)
	}
	spec.Command = path
	if spec.PHPIni != "" {
		if _, err := os.Stat(spec.PHPIni); err != nil {
			return nil, fmt.Errorf("php_ini: %w", err)
		}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	p := &Pool{
		spec:     spec,
		log:      log,
		free:     make(chan *worker, spec.Processes),
		closing:  make(chan struct{}),
		logLimit: lineLimiter{perSecond: 20},
	}
	if runtime.GOOS != "windows" {
		base := os.TempDir()
		if _, err := os.Stat("/tmp"); err == nil {
			base = "/tmp" // short: Unix socket paths are limited to about 104 bytes
		}
		if p.dir, err = os.MkdirTemp(base, "lotus-php-"); err != nil {
			return nil, fmt.Errorf("creating the socket directory: %w", err)
		}
	}
	for i := 0; i < spec.Processes; i++ {
		w := &worker{p: p, id: i, first: make(chan error, 1)}
		p.workers = append(p.workers, w)
		p.wg.Add(1)
		go w.run()
	}
	var failure error
	for _, w := range p.workers {
		if err := <-w.first; err != nil && failure == nil {
			failure = err
		}
	}
	if failure != nil {
		p.Close()
		return nil, failure
	}
	log.Info("php-cgi pool started", "command", spec.Command, "processes", spec.Processes, "max_requests", spec.MaxRequests)
	return p, nil
}

// Alive reports how many processes are running now.
func (p *Pool) Alive() int {
	n := 0
	for _, w := range p.workers {
		w.mu.Lock()
		if w.alive {
			n++
		}
		w.mu.Unlock()
	}
	return n
}

// RecycleAll replaces every process, one by one as each becomes idle: an idle process is stopped at once and
// started again, a busy one finishes its request first. Requests wait for a free process meanwhile (the pool's
// timeout). It is how the admin's "restart pool" works, and it is safe to call while requests run.
func (p *Pool) RecycleAll() {
	for _, w := range p.workers {
		w.mu.Lock()
		switch {
		case !w.alive || w.retiring:
		case w.busy:
			w.recycle = true
		default:
			w.retiring = true
			if w.cmd != nil && w.cmd.Process != nil {
				_ = w.cmd.Process.Kill()
			}
		}
		w.mu.Unlock()
	}
}

// WaitReplaced waits until RecycleAll has finished: every process is running again and none is waiting to be
// replaced. It gives up after max and says whether it finished (a process busy with a long request is the usual reason
// it does not).
func (p *Pool) WaitReplaced(max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		done := true
		for _, w := range p.workers {
			w.mu.Lock()
			if !w.alive || w.retiring || w.recycle {
				done = false
			}
			w.mu.Unlock()
		}
		if done {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-p.closing:
			return false
		}
	}
}

// Restarts reports how many times a process was started again after exiting.
func (p *Pool) Restarts() int64 { return p.restarts.Load() }

// Do gives the request to an idle process, waiting for one if all are busy.
// The process is held until the response body is closed. A process that
// can't be connected to (it exited a moment ago) is skipped and the request
// goes to another: nothing had been sent, so that is safe.
func (p *Pool) Do(ctx context.Context, req *fastcgi.Request) (*fastcgi.Response, error) {
	wait := time.NewTimer(p.spec.Timeout)
	defer wait.Stop()
	var lastErr error
	for attempt := 0; attempt <= len(p.workers); attempt++ {
		w, c, err := p.take(ctx, wait.C)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		resp, err := c.Do(ctx, req)
		if err != nil {
			p.release(w)
			var de *fastcgi.DialError
			if errors.As(err, &de) && ctx.Err() == nil {
				lastErr = err
				continue
			}
			return nil, err
		}
		resp.Body = &releasingBody{ReadCloser: resp.Body, release: func() { p.release(w) }}
		return resp, nil
	}
	return nil, lastErr
}

// take waits for an idle live worker.
func (p *Pool) take(ctx context.Context, timeout <-chan time.Time) (*worker, *fastcgi.Client, error) {
	for {
		select {
		case <-p.closing: // checked first: select picks at random among ready cases
			return nil, nil, ErrClosed
		default:
		}
		select {
		case w := <-p.free:
			w.mu.Lock()
			w.inFree = false
			if !w.alive {
				w.mu.Unlock() // died while idle: not offered again until it is back
				continue
			}
			w.busy = true
			w.served++
			c := w.client
			w.mu.Unlock()
			return w, c, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-timeout:
			return nil, nil, fmt.Errorf("%w: no php-cgi process became free in time", fastcgi.ErrOverloaded)
		case <-p.closing:
			return nil, nil, ErrClosed
		}
	}
}

// release returns a worker after its request.
func (p *Pool) release(w *worker) {
	w.mu.Lock()
	w.busy = false
	if max := p.spec.MaxRequests; (w.recycle || (max > 0 && w.served >= max)) && w.alive && !w.retiring {
		// Its quota is used up, or RecycleAll asked for it: replace it now, while it is idle.
		w.retiring = true
		if w.cmd != nil && w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
	}
	offer := w.alive && !w.retiring && !w.inFree
	if offer {
		w.inFree = true
	}
	w.mu.Unlock()
	if offer {
		p.free <- w // never blocks: at most one entry per worker
	}
}

type releasingBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

// Close stops every process and waits for them. Requests in progress fail.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		close(p.closing)
		for _, w := range p.workers {
			w.mu.Lock()
			if w.cmd != nil && w.cmd.Process != nil {
				_ = w.cmd.Process.Kill()
			}
			w.mu.Unlock()
		}
		p.wg.Wait()
		for _, w := range p.workers {
			w.mu.Lock()
			w.alive = false
			if w.client != nil {
				w.client.Close()
			}
			w.mu.Unlock()
		}
		if p.dir != "" {
			os.RemoveAll(p.dir)
		}
	})
}

// ---- one worker ----

// run is the worker's life: start the process, wait for it to end, start it
// again. The first start is reported to Start.
func (w *worker) run() {
	defer w.p.wg.Done()
	exited, started, err := w.start()
	w.first <- err
	if err != nil {
		return
	}
	backoff := minBackoff
	for {
		var exitErr error
		select {
		case exitErr = <-exited:
		case <-w.p.closing:
			<-exited // Close has killed it; wait until it is really gone
			return
		}
		w.mu.Lock()
		retired := w.retiring
		w.mu.Unlock()
		w.markDead()
		lived := time.Since(started)
		crashed := !retired && (exitErr != nil || lived < time.Second)
		if retired {
			w.p.log.Debug("php-cgi process replaced after max_requests", "worker", w.id)
		} else {
			w.p.log.Log(context.Background(), levelFor(crashed), "php-cgi process exited", "worker", w.id, "error", exitErr, "lived", lived.Round(time.Millisecond))
		}

		delay := time.Duration(0) // a retired process is replaced at once
		if crashed {
			if lived >= stableTime {
				backoff = minBackoff
			}
			delay, backoff = backoff, min(backoff*2, maxBackoff)
		} else {
			backoff = minBackoff
		}
		for {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-w.p.closing:
					return
				}
			}
			select {
			case <-w.p.closing:
				return
			default:
			}
			if exited, started, err = w.start(); err == nil {
				w.p.restarts.Add(1)
				break
			}
			w.p.log.Error("php-cgi process could not be restarted", "worker", w.id, "error", err, "retry_in", backoff)
			delay, backoff = backoff, min(backoff*2, maxBackoff)
		}
	}
}

func levelFor(crashed bool) slog.Level {
	if crashed {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// start launches the process and waits until it accepts connections. It
// returns a channel that delivers the process's exit.
func (w *worker) start() (exited <-chan error, started time.Time, err error) {
	p := w.p
	network, addr, err := w.address()
	if err != nil {
		return nil, time.Time{}, err
	}
	args := []string{"-b", addr}
	if p.spec.PHPIni != "" {
		args = append(args, "-c", p.spec.PHPIni)
	}
	cmd := exec.Command(p.spec.Command, args...)
	cmd.Env = childEnv(p.spec)
	cmd.SysProcAttr = procAttr()
	out := &lineLogger{p: p, worker: w.id}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, time.Time{}, fmt.Errorf("starting %s: %w", p.spec.Command, err)
	}
	confine(cmd.Process)
	started = time.Now()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		out.flush()
	}()
	w.mu.Lock()
	w.cmd = cmd
	w.mu.Unlock()

	// Ready: the process accepts a connection. If it exits first, or takes too
	// long, the start failed.
	deadline := time.NewTimer(p.spec.ReadyTimeout)
	defer deadline.Stop()
	for {
		conn, derr := net.DialTimeout(network, addr, 200*time.Millisecond)
		if derr == nil {
			conn.Close()
			break
		}
		select {
		case waitErr := <-done:
			if waitErr == nil {
				waitErr = errors.New("exited")
			}
			return nil, time.Time{}, fmt.Errorf("%s stopped before it was ready: %w", p.spec.Command, waitErr)
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-done
			return nil, time.Time{}, fmt.Errorf("%s did not accept connections on %s within %s", p.spec.Command, addr, p.spec.ReadyTimeout)
		case <-p.closing:
			_ = cmd.Process.Kill()
			<-done
			return nil, time.Time{}, ErrClosed
		case <-time.After(25 * time.Millisecond):
		}
	}

	client := &fastcgi.Client{Network: network, Address: addr, Timeout: p.spec.Timeout, MaxIdle: 1, MaxConns: 1}
	w.mu.Lock()
	old := w.client
	w.client = client
	w.alive = true
	w.served, w.retiring, w.recycle = 0, false, false
	offer := !w.busy && !w.inFree
	if offer {
		w.inFree = true
	}
	w.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if offer {
		p.free <- w
	}
	return done, started, nil
}

func (w *worker) markDead() {
	w.mu.Lock()
	w.alive = false
	c := w.client
	w.mu.Unlock()
	if c != nil {
		c.Close() // its idle connections are to a process that is gone
	}
}

// address picks where this incarnation of the process listens.
func (w *worker) address() (network, addr string, err error) {
	if w.p.dir != "" {
		path := filepath.Join(w.p.dir, "w"+strconv.Itoa(w.id)+".sock")
		os.Remove(path)
		return "unix", path, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("finding a free port: %w", err)
	}
	addr = ln.Addr().String()
	ln.Close()
	return "tcp", addr, nil
}

// childEnv is the environment of a php-cgi process: a short list of what a
// program needs to run, not the server's own. The server's environment can hold
// secrets (database passwords read with env() in the configuration) that PHP
// scripts must not get for free.
func childEnv(s Spec) []string {
	keep := []string{"PATH", "PATHEXT", "SystemRoot", "SystemDrive", "windir", "ComSpec", "TEMP", "TMP", "TMPDIR",
		"ProgramData", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "PHPRC", "PHP_INI_SCAN_DIR"}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, k := range keep {
			if strings.EqualFold(name, k) {
				env = append(env, kv)
				break
			}
		}
	}
	// Recycling is the pool's job (see Spec.MaxRequests); 0 turns php-cgi's own off.
	env = append(env, "PHP_FCGI_MAX_REQUESTS=0")
	return append(env, s.ExtraEnv...)
}

// ---- output of the processes ----

// lineLogger turns what a process prints into log lines, a few per second at
// most so a script that logs in a loop can't fill the disk.
type lineLogger struct {
	p      *Pool
	worker int
	mu     sync.Mutex
	buf    []byte
}

func (l *lineLogger) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 8192 { // a line without end
		l.emit(string(l.buf))
		l.buf = nil
	}
	return len(b), nil
}

func (l *lineLogger) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.emit(string(l.buf))
		l.buf = nil
	}
}

func (l *lineLogger) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return
	}
	if ok, held := l.p.logLimit.allow(); ok {
		attrs := []any{"worker", l.worker, "output", line}
		if held > 0 {
			attrs = append(attrs, "lines_not_logged", held)
		}
		l.p.log.Warn("php-cgi", attrs...)
	}
}

// lineLimiter allows perSecond events each second and counts the rest.
type lineLimiter struct {
	perSecond int
	mu        sync.Mutex
	window    time.Time
	used      int
	held      int
}

func (l *lineLimiter) allow() (ok bool, held int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.window) >= time.Second {
		l.window, l.used = now, 0
	}
	if l.used >= l.perSecond {
		l.held++
		return false, 0
	}
	l.used++
	held, l.held = l.held, 0
	return true, held
}
