package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Scenario is one thing to measure: a request, repeated.
type Scenario struct {
	Name   string
	Path   string
	Header map[string]string
	Note   string
}

var scenarios = []Scenario{
	{Name: "ping", Path: "/ping", Note: "a fixed answer from a route: the cost of the server itself"},
	{Name: "static-1k", Path: "/small.txt", Note: "a 1 KiB file"},
	{Name: "static-100k", Path: "/medium.bin", Note: "a 100 KiB file that does not compress"},
	{Name: "static-gzip", Path: "/text.txt", Header: map[string]string{"Accept-Encoding": "gzip"}, Note: "a 100 KiB text file, compressed on the fly"},
	{Name: "proxy", Path: "/proxy/x", Note: "a proxied 1 KiB answer"},
	{Name: "proxy-cached", Path: "/cached/x", Note: "the same answer from the response cache"},
	{Name: "not-found", Path: "/no/such/file", Note: "a 404 for a file that doesn't exist"},
}

// Machine says where the numbers come from; numbers from different machines
// are not comparable.
type Machine struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	CPUs  int    `json:"cpus"`
	Go    string `json:"go"`
	Lotus string `json:"lotus"`
}

// Result is the measurement of one scenario.
type Result struct {
	Name     string  `json:"name"`
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	RPS      float64 `json:"rps"`
	P50ms    float64 `json:"p50_ms"`
	P90ms    float64 `json:"p90_ms"`
	P99ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
	MBps     float64 `json:"mb_per_s"`
}

// Report is a whole run.
type Report struct {
	Machine         Machine  `json:"machine"`
	Connections     int      `json:"connections"`
	DurationSeconds float64  `json:"duration_seconds"`
	Results         []Result `json:"results"`
}

// measure runs one scenario: connections workers send requests one after the
// other (closed loop) for the warm-up and then for the measured time.
func measure(base string, sc Scenario, conns int, warmup, duration time.Duration) Result {
	tr := &http.Transport{
		MaxIdleConns: conns, MaxIdleConnsPerHost: conns, MaxConnsPerHost: conns,
		DisableCompression: true, // the scenario decides what to accept
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	type tally struct {
		lat    []time.Duration
		errors int
		bytes  int64
	}
	phase := func(d time.Duration, record bool) (tallies []*tally, elapsed time.Duration) {
		start := time.Now()
		end := start.Add(d)
		tallies = make([]*tally, conns)
		var wg sync.WaitGroup
		for i := 0; i < conns; i++ {
			t := &tally{}
			tallies[i] = t
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(end) {
					req, _ := http.NewRequest(http.MethodGet, base+sc.Path, nil)
					req.Host = "bench.test"
					for k, v := range sc.Header {
						req.Header.Set(k, v)
					}
					t0 := time.Now()
					resp, err := client.Do(req)
					if err != nil {
						t.errors++
						continue
					}
					n, err := io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					lat := time.Since(t0)
					if err != nil || resp.StatusCode >= 500 || (resp.StatusCode >= 400 && sc.Name != "not-found") {
						t.errors++
						continue
					}
					if record {
						t.lat = append(t.lat, lat)
						t.bytes += n
					}
				}
			}()
		}
		wg.Wait()
		return tallies, time.Since(start)
	}

	phase(warmup, false)
	tallies, elapsed := phase(duration, true)
	var all []time.Duration
	res := Result{Name: sc.Name}
	var bytes int64
	for _, t := range tallies {
		all = append(all, t.lat...)
		res.Errors += t.errors
		bytes += t.bytes
	}
	res.Requests = len(all)
	res.RPS = round(float64(len(all))/elapsed.Seconds(), 0)
	res.P50ms = round(ms(Percentile(all, 50)), 3)
	res.P90ms = round(ms(Percentile(all, 90)), 3)
	res.P99ms = round(ms(Percentile(all, 99)), 3)
	res.MaxMs = round(ms(Percentile(all, 100)), 3)
	res.MBps = round(float64(bytes)/elapsed.Seconds()/(1<<20), 1)
	return res
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// Percentile returns the p-th percentile (nearest rank) of the samples; 0 for
// no samples. It does not change its argument.
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}

// Compare lists the scenarios of cur that are worse than in base by more than
// the given percentages: fewer requests per second, or a higher p99. Runs on
// different machines are not comparable, which is reported as a problem too.
func Compare(base, cur Report, maxRPSDrop, maxP99Rise float64) []string {
	var problems []string
	if base.Machine.OS != cur.Machine.OS || base.Machine.Arch != cur.Machine.Arch || base.Machine.CPUs != cur.Machine.CPUs {
		problems = append(problems, fmt.Sprintf("the baseline was made on %s/%s with %d CPUs, this run on %s/%s with %d: they can't be compared",
			base.Machine.OS, base.Machine.Arch, base.Machine.CPUs, cur.Machine.OS, cur.Machine.Arch, cur.Machine.CPUs))
		return problems
	}
	byName := map[string]Result{}
	for _, r := range base.Results {
		byName[r.Name] = r
	}
	for _, r := range cur.Results {
		b, ok := byName[r.Name]
		if !ok {
			continue
		}
		if b.RPS > 0 {
			if drop := (b.RPS - r.RPS) / b.RPS * 100; drop > maxRPSDrop {
				problems = append(problems, fmt.Sprintf("%s: %.0f requests/s, %.0f%% fewer than the baseline's %.0f", r.Name, r.RPS, drop, b.RPS))
			}
		}
		if b.P99ms > 0 {
			if rise := (r.P99ms - b.P99ms) / b.P99ms * 100; rise > maxP99Rise {
				problems = append(problems, fmt.Sprintf("%s: p99 %.2f ms, %.0f%% more than the baseline's %.2f ms", r.Name, r.P99ms, rise, b.P99ms))
			}
		}
	}
	return problems
}
