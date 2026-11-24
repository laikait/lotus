package server

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/proxy"
)

// Reverse proxy (docs/architecture/proxy.md).
//
// A route with `proxy "name"` sends the request to a backend of that upstream.
// The response of the backend goes to the client as it is (error pages never
// replace it, as with PHP), except for what the server itself owns: its own
// Server header and the site's security_headers. Failures of the proxy itself
// (no backend, a timeout, a refused connection) are LOTUS's own 502, 503 and 504
// and go through the site's error pages.

// upstreamConfig translates a configured upstream.
func upstreamConfig(u model.Upstream, log *slog.Logger) (proxy.Config, error) {
	cfg := proxy.Config{
		Name:            u.Name,
		Balance:         u.Balance,
		ConnectTimeout:  time.Duration(u.ConnectTimeout),
		ResponseTimeout: time.Duration(u.ResponseTimeout),
		IdleTimeout:     time.Duration(u.IdleTimeout),
		Retries:         u.Retries,
		MaxFails:        u.MaxFails,
		FailTimeout:     time.Duration(u.FailTimeout),
		MaxConns:        u.MaxConns,
		TLS: proxy.TLSSpec{
			Verify: u.TLSVerify, CAFile: u.TLSCA, ServerName: u.TLSServerName,
			CertFile: u.TLSClientCert, KeyFile: u.TLSClientKey,
		},
	}
	for _, b := range u.Backends {
		spec, err := proxy.ParseBackend(b.Address)
		if err != nil {
			return proxy.Config{}, err
		}
		spec.Weight, spec.MaxConns = b.Weight, b.MaxConns
		cfg.Backends = append(cfg.Backends, spec)
	}
	if h := u.Health; h != nil {
		cfg.Health = &proxy.HealthSpec{
			Path: h.Path, Interval: time.Duration(h.Interval), Timeout: time.Duration(h.Timeout),
			Healthy: h.HealthyThreshold, Unhealthy: h.UnhealthyThreshold,
		}
	}
	if !u.TLSVerify {
		log.Info("upstream certificate verification is off", "upstream", u.Name)
	}
	return cfg, nil
}

// upstreamCloser stops an upstream with its generation.
type upstreamCloser struct{ u *proxy.Upstream }

func (c upstreamCloser) Close() error {
	c.u.Close()
	return nil
}

// serveProxy forwards the request. out is where LOTUS's own errors go (so
// error pages apply); final is where the backend's response goes.
func (rt *siteRuntime) serveProxy(final, out http.ResponseWriter, r *http.Request, a model.Actions, client, reqID string) {
	up := rt.upstreams[a.Proxy]
	if up == nil {
		rt.log.Error("proxy: no such upstream", "upstream", a.Proxy)
		rt.status(out, r, http.StatusInternalServerError, "")
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	rp := &httputil.ReverseProxy{
		Transport: up,
		ErrorLog:  log.New(io.Discard, "", 0),
		Rewrite: func(pr *httputil.ProxyRequest) {
			in, o := pr.In, pr.Out
			// The router's view of the URL (after rewrites) goes upstream; the
			// backend address is chosen per attempt by the upstream.
			o.URL.Scheme, o.URL.Host = "http", "upstream"
			o.URL.Path, o.URL.RawPath, o.URL.RawQuery = in.URL.Path, "", in.URL.RawQuery
			if a.ProxyHostUpstream {
				o.Host = ""
			} else {
				o.Host = in.Host
			}
			// ReverseProxy has removed the client's forwarding headers; what the
			// backend sees is what LOTUS knows, never what the client claims.
			h := o.Header
			h.Del("X-Real-Ip")
			h.Del("X-Forwarded-Port")
			h.Set("X-Forwarded-For", client)
			h.Set("X-Forwarded-Host", in.Host)
			h.Set("X-Forwarded-Proto", scheme)
			if reqID != "" {
				h.Set("X-Request-Id", reqID)
			}
			for _, ph := range a.ProxyHeaders {
				if ph.Value == "" {
					h.Del(ph.Name)
				} else {
					h.Set(ph.Name, ph.Value)
				}
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Server")
			for name := range rt.protected {
				resp.Header.Del(name) // the administrator's security_headers are not overridable
			}
			return nil
		},
		ErrorHandler: func(_ http.ResponseWriter, req *http.Request, err error) {
			rt.proxyFailure(out, req, a.Proxy, err)
		},
	}
	rp.ServeHTTP(final, r)
}

// proxyFailure answers a request the proxy could not complete.
func (rt *siteRuntime) proxyFailure(out http.ResponseWriter, r *http.Request, upstream string, err error) {
	var tooBig *http.MaxBytesError
	var ne net.Error
	status := http.StatusBadGateway
	switch {
	case errors.As(err, &tooBig):
		rt.status(out, r, http.StatusRequestEntityTooLarge, "")
		return
	case r.Context().Err() != nil:
		out.WriteHeader(499) // the client is gone; what Nginx logs
		return
	case errors.Is(err, proxy.ErrNoBackend), errors.Is(err, proxy.ErrBusy):
		status = http.StatusServiceUnavailable
		out.Header().Set("Retry-After", "1")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		status = http.StatusGatewayTimeout
	}
	rt.log.Warn("proxy: request failed", "upstream", upstream, "status", status, "error", err)
	rt.status(out, r, status, "")
}
