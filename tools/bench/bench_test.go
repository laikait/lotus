package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 100; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	// Shuffle-proof: the function sorts a copy.
	s[0], s[99] = s[99], s[0]
	for p, want := range map[float64]time.Duration{50: 50 * time.Millisecond, 90: 90 * time.Millisecond, 99: 99 * time.Millisecond, 100: 100 * time.Millisecond, 1: time.Millisecond, 0: time.Millisecond} {
		if got := Percentile(s, p); got != want {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
	if s[0] != 100*time.Millisecond {
		t.Error("the input must not be sorted in place")
	}
	if Percentile(nil, 50) != 0 {
		t.Error("no samples")
	}
	if got := Percentile([]time.Duration{7 * time.Millisecond}, 99); got != 7*time.Millisecond {
		t.Errorf("one sample: %v", got)
	}
}

func report(cpus int, rps, p99 float64) Report {
	return Report{Machine: Machine{OS: "linux", Arch: "amd64", CPUs: cpus}, Results: []Result{{Name: "ping", RPS: rps, P99ms: p99}}}
}

func TestCompare(t *testing.T) {
	base := report(8, 10000, 10)
	if p := Compare(base, report(8, 9000, 12), 25, 50); len(p) != 0 {
		t.Errorf("within limits: %v", p)
	}
	if p := Compare(base, report(8, 7000, 10), 25, 50); len(p) != 1 || !strings.Contains(p[0], "30% fewer") {
		t.Errorf("slower: %v", p)
	}
	if p := Compare(base, report(8, 10000, 16), 25, 50); len(p) != 1 || !strings.Contains(p[0], "60% more") {
		t.Errorf("p99: %v", p)
	}
	if p := Compare(base, report(8, 2000, 100), 25, 50); len(p) != 2 {
		t.Errorf("both: %v", p)
	}
	// Faster is never a problem; unknown scenarios are ignored.
	if p := Compare(base, report(8, 50000, 1), 25, 50); len(p) != 0 {
		t.Errorf("faster: %v", p)
	}
	other := report(8, 10000, 10)
	other.Results[0].Name = "other"
	if p := Compare(base, other, 25, 50); len(p) != 0 {
		t.Errorf("new scenario: %v", p)
	}
	// Different machines can't be compared.
	if p := Compare(base, report(4, 10000, 10), 25, 50); len(p) != 1 || !strings.Contains(p[0], "can't be compared") {
		t.Errorf("machines: %v", p)
	}
}

func TestPseudoRandomIsStableAndIncompressible(t *testing.T) {
	a, b := pseudoRandom(4096), pseudoRandom(4096)
	if !bytes.Equal(a, b) {
		t.Error("must be the same every run")
	}
	seen := map[byte]bool{}
	for _, c := range a {
		seen[c] = true
	}
	if len(seen) < 200 {
		t.Errorf("only %d distinct byte values", len(seen))
	}
}

// A real, very short run against a real lotus program.
func TestSmokeRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the server")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs the go command to build the server")
	}
	exe := filepath.Join(t.TempDir(), "lotus")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, "../../cmd/lotus")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	jsonPath := filepath.Join(t.TempDir(), "out.json")
	var out, errOut bytes.Buffer
	code := run([]string{"-lotus", exe, "-duration", "300ms", "-warmup", "100ms", "-connections", "4", "-json", jsonPath, "-only", "ping,static-1k,proxy-cached"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d\n%s\n%s", code, out.String(), errOut.String())
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 3 || rep.Connections != 4 || rep.Machine.CPUs == 0 || !strings.Contains(rep.Machine.Lotus, "lotus") {
		t.Fatalf("%+v", rep)
	}
	for _, r := range rep.Results {
		if r.Requests == 0 || r.RPS == 0 || r.Errors != 0 || r.P50ms < 0 || r.P99ms <= 0 || r.P99ms < r.P50ms {
			t.Errorf("%+v", r)
		}
	}
	// Compared with itself there is no regression.
	code = run([]string{"-lotus", exe, "-duration", "300ms", "-warmup", "100ms", "-connections", "4", "-only", "ping", "-baseline", jsonPath, "-max-rps-drop", "95", "-max-p99-rise", "2000"}, &out, &errOut)
	if code != 0 {
		t.Errorf("against its own baseline: %d\n%s", code, errOut.String())
	}
	if code := run([]string{}, &out, &errOut); code == 0 {
		t.Error("-lotus is required")
	}
}
