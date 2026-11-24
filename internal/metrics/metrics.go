// Package metrics writes metrics in the Prometheus text exposition format
// (version 0.0.4), which Prometheus, VictoriaMetrics, Grafana Agent, Datadog and
// most other collectors read.
//
// It is deliberately small: LOTUS keeps its own counters (atomic integers next
// to the code they count) and, when asked, describes them to a Writer. There is
// no registry of metric objects, no global state, and no dependency.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ContentType is the media type of what a Writer produces.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Label is one name and value pair of a series.
type Label struct{ Name, Value string }

// L is a shorthand for building a Label.
func L(name, value string) Label { return Label{name, value} }

// Writer writes metric families. Each family (a name) must be written in one
// go: its HELP and TYPE lines come first, then its series. A family written
// twice, or a name that isn't a valid metric name, is a programming error and
// makes the Writer report it (Err), so a test notices.
type Writer struct {
	w    io.Writer
	err  error
	seen map[string]bool
	cur  string
}

// NewWriter returns a Writer that writes to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w, seen: map[string]bool{}} }

// Err returns the first error: a failed write, or a misuse.
func (w *Writer) Err() error { return w.err }

func (w *Writer) fail(format string, args ...any) {
	if w.err == nil {
		w.err = fmt.Errorf(format, args...)
	}
}

func (w *Writer) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.w, format, args...)
}

// Family starts a metric family. typ is "counter", "gauge" or "histogram".
func (w *Writer) Family(name, typ, help string) {
	if !validName(name) {
		w.fail("metrics: invalid metric name %q", name)
		return
	}
	if w.seen[name] {
		w.fail("metrics: family %q written twice", name)
		return
	}
	w.seen[name] = true
	w.cur = name
	w.printf("# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
}

// Sample writes one series of the current family.
func (w *Writer) Sample(value float64, labels ...Label) {
	w.printf("%s%s %s\n", w.cur, formatLabels(labels), formatValue(value))
}

// Counter writes a whole counter family with one series.
func (w *Writer) Counter(name, help string, value float64, labels ...Label) {
	w.Family(name, "counter", help)
	w.Sample(value, labels...)
}

// Gauge writes a whole gauge family with one series.
func (w *Writer) Gauge(name, help string, value float64, labels ...Label) {
	w.Family(name, "gauge", help)
	w.Sample(value, labels...)
}

// Histogram writes the series of a histogram in the current family:
// cumulative bucket counts for the upper bounds in le (the +Inf bucket is
// added), then the sum and the count. counts[i] is the number of observations
// in bucket i only (not cumulative); the last element is observations above
// the largest bound.
func (w *Writer) Histogram(le []float64, counts []uint64, sum float64, labels ...Label) {
	if len(counts) != len(le)+1 {
		w.fail("metrics: histogram %q has %d counts for %d bounds", w.cur, len(counts), len(le))
		return
	}
	var cum uint64
	for i, bound := range le {
		cum += counts[i]
		w.printf("%s_bucket%s %d\n", w.cur, formatLabels(append(append([]Label(nil), labels...), Label{"le", formatValue(bound)})), cum)
	}
	cum += counts[len(le)]
	w.printf("%s_bucket%s %d\n", w.cur, formatLabels(append(append([]Label(nil), labels...), Label{"le", "+Inf"})), cum)
	w.printf("%s_sum%s %s\n", w.cur, formatLabels(labels), formatValue(sum))
	w.printf("%s_count%s %d\n", w.cur, formatLabels(labels), cum)
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// EscapeLabelValue escapes a label value for the exposition format:
// backslash, double quote and line feed. Anything else, including any UTF-8,
// is allowed.
func EscapeLabelValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func formatLabels(labels []Label) string {
	if len(labels) == 0 {
		return ""
	}
	sorted := make([]Label, len(labels))
	copy(sorted, labels)
	// "le" last, as Prometheus prints it; otherwise by name, so output is stable.
	sort.SliceStable(sorted, func(i, j int) bool {
		if (sorted[i].Name == "le") != (sorted[j].Name == "le") {
			return sorted[j].Name == "le"
		}
		return sorted[i].Name < sorted[j].Name
	})
	var b strings.Builder
	b.WriteByte('{')
	for i, l := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.Name)
		b.WriteString(`="`)
		b.WriteString(EscapeLabelValue(l.Value))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
