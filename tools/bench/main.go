// Command bench measures LOTUS under a fixed set of scenarios and prints the
// result as a table and, with -json, as a file that can be kept and compared
// (docs/benchmarks.md describes the method and its limits).
//
//	go build -o bin/lotus ./cmd/lotus
//	go run ./tools/bench -lotus bin/lotus
//	go run ./tools/bench -lotus bin/lotus -json bench.json -baseline testdata/bench-baseline.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	lotus := fs.String("lotus", "", "the lotus program to measure (required)")
	duration := fs.Duration("duration", 5*time.Second, "measured time of each scenario")
	warmup := fs.Duration("warmup", time.Second, "warm-up time of each scenario, not measured")
	conns := fs.Int("connections", 64, "simultaneous connections")
	only := fs.String("only", "", "comma-separated scenario names to run (default all)")
	jsonOut := fs.String("json", "", "write the results to this file as JSON")
	baseline := fs.String("baseline", "", "compare with the results in this JSON file and fail on a regression")
	maxDrop := fs.Float64("max-rps-drop", 25, "regression limit: percent less requests per second than the baseline")
	maxRise := fs.Float64("max-p99-rise", 50, "regression limit: percent more p99 latency than the baseline")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *lotus == "" {
		fmt.Fprintln(stderr, "bench: -lotus is required (build it first: go build -o bin/lotus ./cmd/lotus)")
		return 1
	}
	exe, err := filepath.Abs(*lotus)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	env, err := startEnv(exe)
	if err != nil {
		fmt.Fprintln(stderr, "bench:", err)
		return 1
	}
	defer env.stop()

	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	rep := Report{Machine: machine(exe), Connections: *conns, DurationSeconds: duration.Seconds()}
	fmt.Fprintf(stdout, "%-14s %10s %10s %10s %10s %10s %8s\n", "scenario", "req/s", "p50 ms", "p90 ms", "p99 ms", "MB/s", "errors")
	for _, sc := range scenarios {
		if len(want) > 0 && !want[sc.Name] {
			continue
		}
		res := measure(env.baseURL, sc, *conns, *warmup, *duration)
		rep.Results = append(rep.Results, res)
		fmt.Fprintf(stdout, "%-14s %10.0f %10.2f %10.2f %10.2f %10.1f %8d\n", res.Name, res.RPS, res.P50ms, res.P90ms, res.P99ms, res.MBps, res.Errors)
	}
	if *jsonOut != "" {
		data, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*jsonOut, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	code := 0
	for _, r := range rep.Results {
		if r.Errors > 0 {
			fmt.Fprintf(stderr, "bench: %s had %d errors; its numbers are not trustworthy\n", r.Name, r.Errors)
			code = 2
		}
	}
	if *baseline != "" {
		data, err := os.ReadFile(*baseline)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var base Report
		if err := json.Unmarshal(data, &base); err != nil {
			fmt.Fprintf(stderr, "bench: %s: %v\n", *baseline, err)
			return 1
		}
		problems := Compare(base, rep, *maxDrop, *maxRise)
		for _, p := range problems {
			fmt.Fprintln(stderr, "regression:", p)
		}
		if len(problems) > 0 {
			code = 3
		} else {
			fmt.Fprintln(stdout, "no regression against", *baseline)
		}
	}
	return code
}

// ---- the thing being measured ----

type env struct {
	baseURL string
	cmd     *exec.Cmd
	backend *http.Server
	dir     string
}

func (e *env) stop() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		_, _ = e.cmd.Process.Wait()
	}
	if e.backend != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.backend.Shutdown(ctx)
	}
	if e.dir != "" {
		_ = os.RemoveAll(e.dir)
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startEnv writes the files the scenarios use, starts a stub backend and the
// lotus program, and waits until it answers.
func startEnv(exe string) (*env, error) {
	e := &env{}
	dir, err := os.MkdirTemp("", "lotus-bench-")
	if err != nil {
		return nil, err
	}
	e.dir = dir
	www := filepath.Join(dir, "www")
	if err := os.MkdirAll(www, 0o755); err != nil {
		return nil, err
	}
	files := map[string][]byte{
		"small.txt":  []byte(strings.Repeat("x", 1024)),
		"medium.bin": pseudoRandom(100 << 10),
		"text.txt":   []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 2300)), // about 100 KB
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(www, n), b, 0o644); err != nil {
			return nil, err
		}
	}

	bp, err := freePort()
	if err != nil {
		return nil, err
	}
	payload := []byte(strings.Repeat("b", 1024))
	e.backend = &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", bp), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write(payload)
	})}
	bl, err := net.Listen("tcp", e.backend.Addr)
	if err != nil {
		e.stop()
		return nil, err
	}
	go func() { _ = e.backend.Serve(bl) }()

	port, err := freePort()
	if err != nil {
		e.stop()
		return nil, err
	}
	cfg := fmt.Sprintf(`lotus_config_version 1

server {
    listen "http" {
        address "127.0.0.1:%d"
    }
    control {
        socket %q
    }
    upstream "stub" {
        backend "http://127.0.0.1:%d"
    }
    cache "c" {
        max_size 64mib
    }
    compression {
        algorithms [ "gzip" ]
        types [ "text/plain" ]
        min_size 1kib
    }
    site "bench.test" {
        root %q
        tls off
        route = "/ping" {
            return 200 "ok"
        }
        route "/proxy/" {
            proxy "stub"
        }
        route "/cached/" {
            proxy "stub"
            cache "c" {
                ttl 1m
            }
        }
    }
}
`, port, filepath.ToSlash(filepath.Join(dir, "control.sock")), bp, filepath.ToSlash(www))
	cfgPath := filepath.Join(dir, "bench.conf")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		e.stop()
		return nil, err
	}
	e.cmd = exec.Command(exe, "start", "-config", cfgPath)
	e.cmd.Stdout, e.cmd.Stderr = io.Discard, io.Discard
	if err := e.cmd.Start(); err != nil {
		e.stop()
		return nil, fmt.Errorf("cannot start %s: %w", exe, err)
	}
	e.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, e.baseURL+"/ping", nil)
		req.Host = "bench.test"
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return e, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.stop()
	return nil, fmt.Errorf("%s did not answer within 10 seconds", exe)
}

// pseudoRandom returns n bytes that don't compress, the same every run.
func pseudoRandom(n int) []byte {
	b := make([]byte, n)
	x := uint64(88172645463325252)
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

func machine(exe string) Machine {
	m := Machine{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Go: runtime.Version()}
	if out, err := exec.Command(exe, "version").Output(); err == nil {
		m.Lotus = strings.TrimSpace(string(out))
	}
	return m
}
