package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/laikait/lotus/internal/control"
)

// exitCertProblem is the exit code of `lotus certs` when a certificate is
// missing, expired or about to expire, so a monitoring check can use it.
const exitCertProblem = 8

// cmdCerts lists the certificates of the running server.
func cmdCerts(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus certs", flag.ContinueOnError)
	socket := controlFlags(fs)
	jsonOut := fs.Bool("json", false, "print the certificates as JSON")
	warn := fs.Int("warn-days", control.CertWarnDays, "report a certificate as expiring when it ends within this many days")
	if !parseNoArgs(fs, args, stderr, "lotus certs [--control SOCKET] [--json] [--warn-days N]") {
		return exitUsage
	}
	if *warn < 0 {
		fmt.Fprintln(stderr, "lotus: --warn-days must not be negative")
		return exitUsage
	}
	c, code := dial(*socket, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	var certs []control.Certificate
	if err := c.Call(control.MethodCertList, nil, &certs); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitCodeFor(err)
	}
	problems := writeCerts(stdout, certs, time.Now(), time.Duration(*warn)*24*time.Hour, *jsonOut)
	if problems > 0 {
		fmt.Fprintf(stderr, "lotus: %d certificate(s) need attention\n", problems)
		return exitCertProblem
	}
	return exitOK
}

// certRow is a certificate as printed by `lotus certs --json`.
type certRow struct {
	control.Certificate
	DaysLeft *int `json:"days_left,omitempty"` // negative once expired
}

// writeCerts prints certs, judged at now with the given warning window, and
// returns how many are not ok.
func writeCerts(w io.Writer, certs []control.Certificate, now time.Time, warn time.Duration, asJSON bool) (problems int) {
	rows := make([]certRow, len(certs))
	for i, c := range certs {
		r := certRow{Certificate: c}
		ready := c.NotAfter != nil
		var na time.Time
		if ready {
			na = *c.NotAfter
			d := daysUntil(na, now)
			r.DaysLeft = &d
		}
		r.Status = control.CertStatus(ready, na, now, warn)
		if r.Status != control.CertOK {
			problems++
		}
		rows[i] = r
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return problems
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no certificates: no site uses tls manual or tls automatic")
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tSTATUS\tEXPIRES\tLEFT\tISSUER\tCOVERS")
	for _, r := range rows {
		expires, left := "-", "-"
		if r.NotAfter != nil {
			expires = r.NotAfter.UTC().Format("2006-01-02")
			switch d := *r.DaysLeft; {
			case d < 0:
				left = fmt.Sprintf("expired %dd ago", -d)
			default:
				left = fmt.Sprintf("%dd", d)
			}
		}
		source := r.Source
		if r.Managed {
			source += " (renewed by lotus)"
		}
		covers := "-"
		if len(r.Domains) > 0 && !(len(r.Domains) == 1 && r.Domains[0] == r.Name) {
			covers = strings.Join(r.Domains, ", ")
		}
		issuer := r.Issuer
		if issuer == "" {
			issuer = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, source, r.Status, expires, left, issuer, covers)
	}
	tw.Flush()
	return problems
}

// daysUntil is the whole days from now to t, rounded toward the past, so a
// certificate with 36 hours left shows 1d and one that ended an hour ago
// shows -1d.
func daysUntil(t, now time.Time) int {
	d := t.Sub(now)
	days := int(d / (24 * time.Hour))
	if d < 0 && d%(24*time.Hour) != 0 {
		days--
	}
	return days
}
