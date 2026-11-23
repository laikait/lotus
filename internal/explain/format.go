package explain

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// WriteText prints the report for people.
func WriteText(w io.Writer, rep *Report) {
	if rep.Site != "" {
		fmt.Fprintf(w, "site: %s (listener %s)\n\n", rep.Site, rep.Listener)
	}
	for i, s := range rep.Steps {
		fmt.Fprintf(w, "%2d. %-10s %s\n", i+1, s.Phase, s.Detail)
	}
	fmt.Fprintf(w, "\nresult: %s\n", rep.Outcome.Summary())
}

// Summary is the one-line answer.
func (o Outcome) Summary() string {
	var s string
	switch o.Kind {
	case KindFile:
		s = fmt.Sprintf("%d, serves the file %s", o.Status, o.File)
	case KindPHP:
		s = fmt.Sprintf("runs the PHP script %s", o.File)
	case KindRedirect:
		s = fmt.Sprintf("%d %s, redirects to %s", o.Status, http.StatusText(o.Status), o.Location)
	case KindStatus:
		s = fmt.Sprintf("%d %s", o.Status, http.StatusText(o.Status))
		if o.Body != "" {
			s += fmt.Sprintf(", body %q", o.Body)
		}
	case KindDepends:
		s = "depends on the files on disk (not checked)"
	default:
		s = fmt.Sprintf("error %d", o.Status)
	}
	if o.Note != "" {
		s += " (" + strings.TrimSpace(o.Note) + ")"
	}
	return s
}
