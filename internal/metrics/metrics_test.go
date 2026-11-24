package metrics

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// parse reads the output with Prometheus's own parser: if it accepts it and the
// values come out as written, real collectors will too.
func parse(t *testing.T, text string) map[string]*struct {
	typ  string
	help string
} {
	t.Helper()
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Prometheus's parser rejected the output: %v\n%s", err, text)
	}
	out := map[string]*struct {
		typ  string
		help string
	}{}
	for name, f := range fams {
		out[name] = &struct {
			typ  string
			help string
		}{f.GetType().String(), f.GetHelp()}
	}
	return out
}

func TestOutputIsAcceptedByPrometheusParser(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Counter("lotus_requests_total", "Requests.\nSecond line \\ backslash.", 12, L("site", "example.com"), L("code_class", "2xx"))
	w.Gauge("lotus_temperature", "A gauge.", -3.5)
	w.Family("lotus_things", "counter", "Several series.")
	w.Sample(1, L("name", `quote " backslash \ newline `+"\n"+` ünïcode 日本`))
	w.Sample(2, L("name", ""))
	w.Family("lotus_duration_seconds", "histogram", "Durations.")
	w.Histogram([]float64{0.1, 1}, []uint64{3, 2, 1}, 4.5, L("site", "a"))
	w.Histogram([]float64{0.1, 1}, []uint64{0, 0, 0}, 0, L("site", "b"))
	w.Gauge("lotus_inf", "Infinity.", math.Inf(1))
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	fams := parse(t, buf.String())
	for name, typ := range map[string]string{
		"lotus_requests_total": "COUNTER", "lotus_temperature": "GAUGE", "lotus_things": "COUNTER",
		"lotus_duration_seconds": "HISTOGRAM", "lotus_inf": "GAUGE",
	} {
		if f := fams[name]; f == nil || f.typ != typ {
			t.Errorf("%s: %+v", name, f)
		}
	}
}

func TestHistogramIsCumulative(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Family("h", "histogram", "h")
	w.Histogram([]float64{0.1, 1}, []uint64{3, 2, 1}, 4.5)
	want := []string{`h_bucket{le="0.1"} 3`, `h_bucket{le="1"} 5`, `h_bucket{le="+Inf"} 6`, `h_sum 4.5`, `h_count 6`}
	for _, line := range want {
		if !strings.Contains(buf.String(), line+"\n") {
			t.Errorf("missing %q in\n%s", line, buf.String())
		}
	}
}

func TestMisuseIsReported(t *testing.T) {
	for name, f := range map[string]func(*Writer){
		"twice":         func(w *Writer) { w.Gauge("a", "x", 1); w.Gauge("a", "x", 2) },
		"bad name":      func(w *Writer) { w.Gauge("has space", "x", 1) },
		"digit first":   func(w *Writer) { w.Gauge("1abc", "x", 1) },
		"empty":         func(w *Writer) { w.Gauge("", "x", 1) },
		"wrong buckets": func(w *Writer) { w.Family("h", "histogram", "x"); w.Histogram([]float64{1}, []uint64{1}, 0) },
	} {
		w := NewWriter(&bytes.Buffer{})
		f(w)
		if w.Err() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLabelOrderIsStable(t *testing.T) {
	var a, b bytes.Buffer
	NewWriter(&a).Counter("c", "x", 1, L("b", "2"), L("a", "1"))
	NewWriter(&b).Counter("c", "x", 1, L("a", "1"), L("b", "2"))
	if a.String() != b.String() || !strings.Contains(a.String(), `c{a="1",b="2"} 1`) {
		t.Errorf("%q vs %q", a.String(), b.String())
	}
}

func FuzzLabelValuesStayParseable(f *testing.F) {
	for _, s := range []string{"", "a", `"`, `\`, "\n", "\xff\xfe", `}{`, "a\x00b", strings.Repeat("\\\"", 50)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		w.Counter("fuzz_total", "x", 1, L("v", value))
		if w.Err() != nil {
			t.Fatal(w.Err())
		}
		if !utf8.ValidString(value) {
			return // the parser requires valid UTF-8; callers pass names from the configuration
		}
		p := expfmt.NewTextParser(model.UTF8Validation)
		fams, err := p.TextToMetricFamilies(&buf)
		if err != nil {
			t.Fatalf("value %q: %v\n%s", value, err, buf.String())
		}
		got := fams["fuzz_total"].GetMetric()[0].GetLabel()[0].GetValue()
		if got != value {
			t.Fatalf("value %q read back as %q", value, got)
		}
	})
}
