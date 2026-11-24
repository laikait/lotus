package proxy

import (
	"context"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"
)

// Active health checking: every Interval each backend is probed, with an HTTP
// request to Path (any status from 200 to 399 is healthy) or, without a path,
// a TCP connection. A backend changes state only after several results in a
// row (Unhealthy failures to go down, Healthy successes to come back), so one
// slow answer doesn't flap it. All backends start out healthy.

func (u *Upstream) healthLoop(b *backend) {
	defer u.wg.Done()
	h := u.cfg.Health
	interval := h.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	// Don't probe all backends at the same instant.
	if !sleep(u.stop, time.Duration(rand.Int64N(int64(interval/4)+1))) {
		return
	}
	need := func(n, def int) int {
		if n <= 0 {
			return def
		}
		return n
	}
	healthy, unhealthy := need(h.Healthy, 2), need(h.Unhealthy, 3)
	okRun, badRun := 0, 0
	for {
		if u.probe(b) == nil {
			okRun, badRun = okRun+1, 0
			if b.probeDown.Load() && okRun >= healthy {
				b.probeDown.Store(false)
				u.log.Info("backend is healthy again", "backend", b.spec.Address)
			}
		} else {
			okRun, badRun = 0, badRun+1
			if !b.probeDown.Load() && badRun >= unhealthy {
				b.probeDown.Store(true)
				u.log.Warn("backend failed its health checks", "backend", b.spec.Address)
			}
		}
		if !sleep(u.stop, interval) {
			return
		}
	}
}

func sleep(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

type probeError string

func (e probeError) Error() string { return string(e) }

func (u *Upstream) probe(b *backend) error {
	h := u.cfg.Health
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	go func() { // stop a probe when the upstream closes
		select {
		case <-u.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	if h.Path == "" {
		d := net.Dialer{}
		c, err := d.DialContext(ctx, b.spec.Network, b.spec.Address)
		if err != nil {
			return err
		}
		return c.Close()
	}
	scheme := "http"
	if b.spec.TLS {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+b.id+h.Path, nil)
	if err != nil {
		return err
	}
	req.Host = b.auth
	req.Header.Set("User-Agent", "lotus-health-check")
	resp, err := b.tr.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return probeError("unhealthy status " + http.StatusText(resp.StatusCode))
	}
	return nil
}
