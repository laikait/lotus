// Package acme gets and renews certificates automatically, using certmagic.
//
// LOTUS decides which names to manage (the sites that use `tls automatic`) and
// never issues for any other name: there is no on-demand issuance, so a client
// can't make the server ask a CA for an arbitrary host name by sending it in
// the TLS handshake.
//
// Both challenges that need no DNS access work with the server's own
// listeners, without a second one on the same port:
//
//   - HTTP-01: the plain-HTTP handler passes requests for
//     /.well-known/acme-challenge/ to HandleHTTPChallenge.
//   - TLS-ALPN-01: a TLS handshake that offers the protocol "acme-tls/1" is
//     answered with the challenge certificate by GetCertificate.
package acme

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/caddyserver/certmagic"
	acmez "github.com/mholt/acmez/v3/acme"
	"go.uber.org/zap"
)

// Config says how to get certificates.
type Config struct {
	Email string
	// CA is the ACME directory URL; empty is Let's Encrypt production.
	CA string
	// CARoot is a PEM file with the root of a private CA (or a test CA such as
	// Pebble), trusted for talking to the ACME server and nothing else.
	CARoot string
	// EAB is external account binding, for CAs that require it.
	EABKeyID, EABHMACKey string
	// Storage is the directory for account keys, certificates and private keys.
	Storage string
	// HTTPPort and TLSALPNPort are the ports the CA reaches this server on when
	// they are not 80 and 443 (for example behind a port forward, or in tests).
	HTTPPort, TLSALPNPort int
	// Challenges limits the validations offered: "http-01", "tls-alpn-01",
	// "dns-01". Empty means every one that can work (dns-01 needs DNS).
	Challenges []string
	// DNS enables DNS-01, which is what wildcard names need.
	DNS    *DNS
	Logger *slog.Logger
}

// Manager obtains and renews the certificates of the names it is given.
type Manager struct {
	cfg    *certmagic.Config
	cache  *certmagic.Cache
	issuer *certmagic.ACMEIssuer
	log    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	managed map[string]bool
}

// New prepares a manager. It touches the storage directory (and fails if it is
// unusable) but doesn't contact the CA.
func New(c Config) (*Manager, error) {
	if c.Storage == "" {
		return nil, errors.New("acme: no storage directory")
	}
	if err := os.MkdirAll(c.Storage, 0o700); err != nil {
		return nil, fmt.Errorf("acme: storage: %w", err)
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	m := &Manager{log: c.Logger, managed: map[string]bool{}}
	m.ctx, m.cancel = context.WithCancel(context.Background())

	zl := zap.New(newCore(c.Logger.With("component", "acme")))
	storage := &certmagic.FileStorage{Path: c.Storage}

	var mcfg *certmagic.Config
	m.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return mcfg, nil },
		Logger:           zl,
	})
	mcfg = certmagic.New(m.cache, certmagic.Config{Storage: storage, Logger: zl})

	ca := c.CA
	if ca == "" {
		ca = certmagic.LetsEncryptProductionCA
	}
	is := certmagic.ACMEIssuer{
		CA: ca, Email: c.Email, Agreed: true,
		AltHTTPPort: c.HTTPPort, AltTLSALPNPort: c.TLSALPNPort,
		// The server's own listeners answer the challenges, so the issuer must
		// not try to bind the ports (it notices they are taken, but this is explicit).
		DisableDistributedSolvers: false,
		Logger:                    zl,
	}
	if len(c.Challenges) > 0 {
		is.DisableHTTPChallenge = !slices.Contains(c.Challenges, "http-01")
		is.DisableTLSALPNChallenge = !slices.Contains(c.Challenges, "tls-alpn-01")
	}
	if c.DNS != nil && (len(c.Challenges) == 0 || slices.Contains(c.Challenges, "dns-01")) {
		solver, err := c.DNS.solver()
		if err != nil {
			return nil, fmt.Errorf("acme: dns: %w", err)
		}
		is.DNS01Solver = solver
	}
	if c.EABKeyID != "" {
		is.ExternalAccount = &acmez.EAB{KeyID: c.EABKeyID, MACKey: c.EABHMACKey}
	}
	if c.CARoot != "" {
		pem, err := os.ReadFile(c.CARoot)
		if err != nil {
			return nil, fmt.Errorf("acme: ca_root: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("acme: ca_root: %s holds no certificate", c.CARoot)
		}
		is.TrustedRoots = pool
	}
	m.issuer = certmagic.NewACMEIssuer(mcfg, is)
	mcfg.Issuers = []certmagic.Issuer{m.issuer}
	m.cfg = mcfg
	return m, nil
}

// Manage starts obtaining and renewing certificates for names, in the
// background. Names that no public CA can issue for (an IP address, localhost,
// a name without a dot) are returned as skipped, with the reason. Names
// already managed are left alone, so it can be called again after a reload.
func (m *Manager) Manage(names []string) (skipped map[string]string) {
	skipped = map[string]string{}
	var fresh []string
	m.mu.Lock()
	for _, n := range names {
		if m.managed[n] {
			continue
		}
		if !strings.Contains(n, ".") || !certmagic.SubjectQualifiesForPublicCert(n) {
			skipped[n] = "a public certificate authority can't issue for this name (an IP address, localhost or a name without a public domain)"
			continue
		}
		m.managed[n] = true
		fresh = append(fresh, n)
	}
	m.mu.Unlock()
	// Certificates already in storage (from an earlier run) are loaded now, so
	// the first handshakes after a restart are served without waiting for the
	// background work below.
	for _, n := range fresh {
		if _, err := m.cfg.CacheManagedCertificate(m.ctx, n); err != nil {
			m.log.Debug("acme: no stored certificate yet", "name", n, "error", err)
		}
	}
	if len(fresh) > 0 {
		if err := m.cfg.ManageAsync(m.ctx, fresh); err != nil {
			m.log.Error("acme: could not start managing certificates", "names", fresh, "error", err)
		}
	}
	return skipped
}

// GetCertificate returns the certificate for a TLS handshake: the one that was
// obtained for the requested name, or the challenge certificate of a
// TLS-ALPN-01 validation. It never obtains a certificate during the handshake.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return m.cfg.GetCertificate(hello)
}

// IsChallenge reports whether a handshake is a TLS-ALPN-01 validation.
func IsChallenge(hello *tls.ClientHelloInfo) bool {
	for _, p := range hello.SupportedProtos {
		if p == ALPNProtocol {
			return true
		}
	}
	return false
}

// ALPNProtocol is the protocol name of TLS-ALPN-01, to be offered by listeners.
const ALPNProtocol = "acme-tls/1"

// HandleHTTPChallenge answers an HTTP-01 validation request. It reports whether
// the request was one, and was handled.
func (m *Manager) HandleHTTPChallenge(w http.ResponseWriter, r *http.Request) bool {
	return m.issuer.HandleHTTPChallenge(w, r)
}

// LooksLikeHTTPChallenge says whether a request is for the ACME challenge path.
func LooksLikeHTTPChallenge(r *http.Request) bool { return certmagic.LooksLikeHTTPChallenge(r) }

// Close stops renewals and releases the cache. Certificates stay in storage.
func (m *Manager) Close() {
	m.cancel()
	m.cache.Stop()
}
