package acme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/libdns/libdns"
)

// DNS says how DNS-01 challenges are answered: which provider sets the TXT
// record, and how long to wait for it to be visible.
type DNS struct {
	// Provider is "cloudflare" or "exec". Tests may set Custom instead.
	Provider string
	// APIToken is the Cloudflare API token (Zone.DNS:Edit; Zone:Read if the
	// token isn't limited to one zone).
	APIToken string
	// Command is the program of the exec provider. LOTUS runs it as
	// `command present` and `command cleanup`, without a shell, with the record
	// in LOTUS_ACME_* environment variables (see docs/architecture/acme.md).
	Command string
	// PropagationDelay waits before the first check that the record is visible;
	// PropagationTimeout is how long to keep checking (0 is two minutes, negative
	// skips the check). Resolvers are the DNS servers asked for it (empty: the
	// system's).
	PropagationDelay, PropagationTimeout time.Duration
	// SkipPropagationCheck asks the CA without looking for the record first.
	SkipPropagationCheck bool
	Resolvers            []string

	// Custom replaces the providers above (for tests and embedding).
	Custom certmagic.DNSProvider
}

// provider returns the libdns implementation for d.
func (d *DNS) provider() (certmagic.DNSProvider, error) {
	switch {
	case d.Custom != nil:
		return d.Custom, nil
	case d.Provider == "cloudflare":
		if d.APIToken == "" {
			return nil, errors.New("dns provider cloudflare needs an api_token")
		}
		return &cloudflare.Provider{APIToken: d.APIToken}, nil
	case d.Provider == "exec":
		if d.Command == "" {
			return nil, errors.New("dns provider exec needs a command")
		}
		return &execProvider{command: d.Command}, nil
	}
	return nil, fmt.Errorf("unknown dns provider %q", d.Provider)
}

// solver builds the DNS-01 solver for certmagic.
func (d *DNS) solver() (*certmagic.DNS01Solver, error) {
	p, err := d.provider()
	if err != nil {
		return nil, err
	}
	timeout := d.PropagationTimeout
	if d.SkipPropagationCheck {
		timeout = -1 // certmagic's value for "don't check"
	}
	return &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{
		DNSProvider:        p,
		TTL:                60 * time.Second,
		PropagationDelay:   d.PropagationDelay,
		PropagationTimeout: timeout,
		Resolvers:          d.Resolvers,
	}}, nil
}

// execProvider sets and removes the challenge record by running a program the
// administrator named. It exists so that any DNS service with a command-line
// tool or API script can be used without LOTUS knowing it.
//
// The program gets the action as its only argument ("present" or "cleanup")
// and the record in the environment:
//
//	LOTUS_ACME_ZONE   the zone, without the final dot          example.com
//	LOTUS_ACME_NAME   the record's name, without the final dot _acme-challenge.www.example.com
//	LOTUS_ACME_VALUE  the TXT value
//	LOTUS_ACME_TTL    seconds
//
// It succeeds by exiting with status 0. Its output is kept (shortened) for the
// log when it fails.
type execProvider struct{ command string }

const execTimeout = time.Minute

func (p *execProvider) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return p.run(ctx, "present", zone, recs)
}

func (p *execProvider) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return p.run(ctx, "cleanup", zone, recs)
}

func (p *execProvider) run(ctx context.Context, action, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	zone = strings.TrimSuffix(zone, ".")
	for _, rec := range recs {
		rr := rec.RR()
		name := zone
		if rr.Name != "" && rr.Name != "@" {
			name = rr.Name + "." + zone
		}
		ctx, cancel := context.WithTimeout(ctx, execTimeout)
		cmd := exec.CommandContext(ctx, p.command, action)
		cmd.Env = append(os.Environ(),
			"LOTUS_ACME_ZONE="+zone,
			"LOTUS_ACME_NAME="+name,
			"LOTUS_ACME_VALUE="+rr.Data,
			"LOTUS_ACME_TTL="+strconv.Itoa(int(rr.TTL/time.Second)),
		)
		var out bytes.Buffer
		lw := &limitedWriter{w: &out, n: 2048}
		cmd.Stdout, cmd.Stderr = lw, lw // the same writer: exec calls it from one goroutine at a time
		err := cmd.Run()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("dns %s for %s: %s %w: %s", action, name, p.command, err, strings.TrimSpace(out.String()))
		}
	}
	return recs, nil
}

// limitedWriter keeps the first n bytes and drops the rest.
type limitedWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(b[:min(len(b), room)])
	}
	return len(b), nil
}
