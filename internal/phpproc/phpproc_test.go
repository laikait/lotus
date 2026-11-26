package phpproc

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laikait/lotus/internal/fastcgi"
	"github.com/laikait/lotus/internal/fastcgi/fcgitest"
)

// The test binary doubles as a fake php-cgi (see fcgitest.RunChildIfRequested).
func TestMain(m *testing.M) {
	fcgitest.RunChildIfRequested()
	os.Exit(m.Run())
}

func spec(n int) Spec {
	exe, _ := os.Executable()
	return Spec{
		Command:      exe,
		Processes:    n,
		Timeout:      5 * time.Second,
		ReadyTimeout: 10 * time.Second,
		ExtraEnv:     []string{fcgitest.ChildEnv + "=1"},
	}
}

func startPool(t *testing.T, s Spec) *Pool {
	t.Helper()
	p, err := Start(s, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

type answer struct {
	body       string
	concurrent string
}

// call runs a script on the pool and reads the whole answer.
func call(ctx context.Context, p *Pool, script, query string) (answer, error) {
	resp, err := p.Do(ctx, &fastcgi.Request{Params: []fastcgi.Param{
		{Name: "SCRIPT_NAME", Value: script}, {Name: "QUERY_STRING", Value: query},
	}})
	if err != nil {
		return answer{}, err
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return answer{string(b), resp.Header.Get("X-Concurrent")}, err
}

func TestPoolServesRequests(t *testing.T) {
	p := startPool(t, spec(3))
	if p.Alive() != 3 {
		t.Fatalf("%d processes alive, want 3", p.Alive())
	}
	pids := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := call(context.Background(), p, "/slow", "")
			if err != nil {
				t.Error(err)
				return
			}
			if a.concurrent != "1" {
				t.Errorf("a process was given %s requests at once; php-cgi handles one", a.concurrent)
			}
			mu.Lock()
			pids[a.body] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(pids) != 3 {
		t.Errorf("requests ran in %d processes, want all 3: %v", len(pids), pids)
	}
}

func TestOneProcessHandlesRequestsOneAtATime(t *testing.T) {
	p := startPool(t, spec(1))
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := call(context.Background(), p, "/slow", "")
			if err != nil || a.concurrent != "1" {
				t.Errorf("%v %+v", err, a)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < 550*time.Millisecond {
		t.Errorf("two 300 ms requests finished in %v: they ran together", d)
	}
}

func TestWaitingHonoursTheContext(t *testing.T) {
	p := startPool(t, spec(1))
	go call(context.Background(), p, "/slow", "")
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := call(ctx, p, "/pid", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v", err)
	}
}

func TestACrashedProcessIsReplaced(t *testing.T) {
	p := startPool(t, spec(1))
	first, err := call(context.Background(), p, "/pid", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(context.Background(), p, "/die", ""); err == nil {
		t.Fatal("a request that killed the process succeeded")
	}
	// The pool brings a new process up; requests work again, in a new process.
	var second answer
	deadline := time.Now().Add(10 * time.Second)
	for {
		second, err = call(context.Background(), p, "/pid", "")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the process was never replaced: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if second.body == first.body {
		t.Error("the same process id after a crash")
	}
	// The pool counts the restart just after the new process is up, so a quick request can be answered a moment
	// before the counter moves: wait for it instead of reading it once.
	for end := time.Now().Add(2 * time.Second); p.Restarts() < 1 && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if p.Restarts() < 1 {
		t.Errorf("restarts = %d", p.Restarts())
	}
}

func TestProcessesAreRecycledAfterMaxRequests(t *testing.T) {
	s := spec(2)
	s.MaxRequests = 3
	p := startPool(t, s)
	pids := map[string]int{}
	for i := 0; i < 40; i++ {
		a, err := call(context.Background(), p, "/pid", "")
		if err != nil {
			t.Fatalf("request %d: %v (a retiring process must never be handed a request)", i, err)
		}
		pids[a.body]++
	}
	for pid, n := range pids {
		if n > 3 {
			t.Errorf("process %s served %d requests, the limit is 3", pid, n)
		}
	}
	if len(pids) < 10 {
		t.Errorf("only %d distinct processes served 40 requests", len(pids))
	}
	if p.Restarts() < 10 {
		t.Errorf("restarts = %d", p.Restarts())
	}
}

func TestRecyclingUnderConcurrentLoad(t *testing.T) {
	s := spec(4)
	s.MaxRequests = 7
	p := startPool(t, s)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if a, err := call(context.Background(), p, "/pid", ""); err != nil || a.concurrent != "1" {
					t.Errorf("%v %+v", err, a)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCloseStopsEveryProcess(t *testing.T) {
	p, err := Start(spec(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	for i, w := range p.workers {
		w.mu.Lock()
		gone := w.cmd != nil && w.cmd.ProcessState != nil
		w.mu.Unlock()
		if !gone {
			t.Errorf("process %d is still running after Close", i)
		}
	}
	if _, err := call(context.Background(), p, "/pid", ""); !errors.Is(err, ErrClosed) {
		t.Errorf("a request after Close got %v", err)
	}
	if p.dir != "" {
		if _, err := os.Stat(p.dir); err == nil {
			t.Error("the socket directory was left behind")
		}
	}
	p.Close() // twice is fine
}

func TestStartFailuresAreClearAndLeaveNothingRunning(t *testing.T) {
	t.Run("missing program", func(t *testing.T) {
		s := spec(1)
		s.Command = filepath.Join(t.TempDir(), "no-such-php-cgi")
		if _, err := Start(s, nil); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("exits at once", func(t *testing.T) {
		s := spec(2)
		s.ExtraEnv = []string{fcgitest.ChildEnv + "=exit"}
		if _, err := Start(s, nil); err == nil || !strings.Contains(err.Error(), "stopped before it was ready") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("never ready", func(t *testing.T) {
		s := spec(2)
		s.ExtraEnv = []string{fcgitest.ChildEnv + "=hang"}
		s.ReadyTimeout = 400 * time.Millisecond
		start := time.Now()
		_, err := Start(s, nil)
		if err == nil || !strings.Contains(err.Error(), "did not accept connections") {
			t.Errorf("got %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("took %v", d)
		}
	})
	t.Run("missing php.ini", func(t *testing.T) {
		s := spec(1)
		s.PHPIni = filepath.Join(t.TempDir(), "nope.ini")
		if _, err := Start(s, nil); err == nil || !strings.Contains(err.Error(), "php_ini") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("no processes", func(t *testing.T) {
		if _, err := Start(spec(0), nil); err == nil {
			t.Error("zero processes were accepted")
		}
	})
}

func TestTheProcessesGetAMinimalEnvironment(t *testing.T) {
	t.Setenv("LOTUS_TEST_SECRET", "database-password")
	p := startPool(t, spec(1))
	if a, err := call(context.Background(), p, "/env", "LOTUS_TEST_SECRET"); err != nil || a.body != "" {
		t.Errorf("the server's environment leaked into PHP: %q (%v)", a.body, err)
	}
	if a, err := call(context.Background(), p, "/env", "PHP_FCGI_MAX_REQUESTS"); err != nil || a.body != "0" {
		t.Errorf("php-cgi's own recycling must be off, the pool does it: %q (%v)", a.body, err)
	}
	if a, err := call(context.Background(), p, "/env", "PATH"); err != nil || a.body == "" {
		t.Errorf("PATH must be passed on: %q (%v)", a.body, err)
	}
}

func TestPHPIniIsPassedToPHP(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "php.ini")
	must(t, os.WriteFile(ini, []byte("; empty\n"), 0o644))
	s := spec(1)
	s.PHPIni = ini
	p := startPool(t, s)
	a, err := call(context.Background(), p, "/args", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.body, "-b ") || !strings.Contains(a.body, "-c "+ini) {
		t.Errorf("php-cgi arguments: %q", a.body)
	}
}

func TestAWorkerIsHeldUntilTheBodyIsClosed(t *testing.T) {
	s := spec(1)
	s.Timeout = 150 * time.Millisecond
	p := startPool(t, s)
	resp, err := p.Do(context.Background(), &fastcgi.Request{Params: []fastcgi.Param{{Name: "SCRIPT_NAME", Value: "/pid"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Not closed: the only process is still taken.
	if _, err := call(context.Background(), p, "/pid", ""); !errors.Is(err, fastcgi.ErrOverloaded) {
		t.Errorf("got %v, want the pool to be exhausted", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	resp.Body.Close() // closing twice must not hand the process out twice
	if _, err := call(context.Background(), p, "/pid", ""); err != nil {
		t.Errorf("after Close: %v", err)
	}
	if len(p.free) > 1 {
		t.Errorf("%d entries for one worker", len(p.free))
	}
}

func TestChildEnvironmentOnlyHoldsWhatIsAllowed(t *testing.T) {
	t.Setenv("LOTUS_TEST_SECRET", "x")
	t.Setenv("PHPRC", "/etc/php")
	env := childEnv(Spec{ExtraEnv: []string{"A=1"}})
	has := func(prefix string) bool {
		for _, kv := range env {
			if strings.HasPrefix(strings.ToUpper(kv), strings.ToUpper(prefix)) {
				return true
			}
		}
		return false
	}
	if has("LOTUS_TEST_SECRET=") {
		t.Error("an arbitrary variable was passed on")
	}
	if !has("PHPRC=") || !has("A=1") || !has("PHP_FCGI_MAX_REQUESTS=0") {
		t.Errorf("missing variables: %v", env)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecycleAllReplacesEveryProcess(t *testing.T) {
	p := startPool(t, spec(3))
	before := map[string]bool{}
	for i := 0; i < 30 && len(before) < 3; i++ {
		a, err := call(context.Background(), p, "/slow", "")
		if err != nil {
			t.Fatal(err)
		}
		before[a.body] = true
	}
	// One request is running while the pool is recycled: it must finish.
	done := make(chan error, 1)
	go func() { _, err := call(context.Background(), p, "/slow", ""); done <- err }()
	time.Sleep(50 * time.Millisecond)
	p.RecycleAll()
	if err := <-done; err != nil {
		t.Errorf("the request that was running during the recycle failed: %v", err)
	}
	// After the replacement every process has a new id, and requests keep working.
	deadline := time.Now().Add(15 * time.Second)
	for p.Alive() < 3 || p.Restarts() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("pool not back: alive=%d restarts=%d", p.Alive(), p.Restarts())
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i := 0; i < 12; i++ {
		a, err := call(context.Background(), p, "/pid", "")
		if err != nil {
			t.Fatalf("request after the recycle: %v", err)
		}
		if before[a.body] {
			t.Errorf("process %s survived the recycle", a.body)
		}
	}
}
