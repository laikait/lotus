package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	"github.com/laikait/lotus/internal/acme"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/vhost"
)

// certWarnWindow is how long before expiry a certificate is reported as expiring.
const certWarnWindow = 14 * 24 * time.Hour

// loadCertificate reads the certificate and key of a manual-TLS site. It
// reports (as warnings, not errors) certificates that don't cover the site's
// names, are expired, or expire soon; those are mistakes worth seeing at
// startup, but not always fatal (for example a certificate renewed out of band).
func loadCertificate(site *model.Site, log *slog.Logger, now time.Time) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(site.Certificate, site.CertificateKey)
	if err != nil {
		// The error names the files, never the key material.
		return nil, fmt.Errorf("site %q: loading certificate: %w", site.Name, err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("site %q: certificate file contains no certificate", site.Name)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("site %q: parsing certificate: %w", site.Name, err)
	}
	cert.Leaf = leaf

	for _, name := range append([]string{site.Name}, site.Aliases...) {
		check := name
		if len(name) > 2 && name[:2] == "*." {
			check = "x" + name[1:] // any label stands in for the wildcard
		}
		if err := leaf.VerifyHostname(check); err != nil {
			log.Warn("certificate does not cover a name of the site",
				"site", site.Name, "name", name, "certificate_names", leaf.DNSNames)
		}
	}
	switch {
	case now.After(leaf.NotAfter):
		log.Warn("certificate has expired", "site", site.Name, "not_after", leaf.NotAfter.Format(time.RFC3339))
	case leaf.NotAfter.Sub(now) < certWarnWindow:
		log.Warn("certificate expires soon", "site", site.Name, "not_after", leaf.NotAfter.Format(time.RFC3339))
	}
	return &cert, nil
}

func tlsVersion(v string) uint16 {
	if v == "1.3" {
		return tls.VersionTLS13
	}
	return tls.VersionTLS12
}

// newTLSConfig builds the TLS configuration of one https listener. The
// certificate is chosen by SNI with the same rules as the Host header: exact
// name, longest wildcard, then the listener's default site. HTTP/2 is
// negotiated by net/http through ALPN.
//
// table returns the listener's current virtual host table, so a reload
// changes the certificates served to new handshakes.
//
// acmeMgr returns the manager of automatic certificates (nil when no site uses
// them). It answers TLS-ALPN-01 validations, and supplies the certificate of
// sites with `tls automatic`. A certificate is never obtained during a
// handshake, and only names of configured sites are ever looked up.
func newTLSConfig(minVersion string, table func() *vhost.Table[*siteRuntime], acmeMgr func() *acme.Manager) *tls.Config {
	return &tls.Config{
		MinVersion: tlsVersion(minVersion),
		// The validation protocol must be negotiable; HTTP/2 and HTTP/1.1 are
		// added by net/http.
		NextProtos: []string{acme.ALPNProtocol},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			mgr := acmeMgr()
			if mgr != nil && acme.IsChallenge(hello) {
				return mgr.GetCertificate(hello)
			}
			name, _ := vhost.NormalizeHost(hello.ServerName) // empty (no SNI) picks the default site
			site, _, ok := table().Lookup(name)
			if !ok {
				return nil, fmt.Errorf("no certificate for %q", hello.ServerName)
			}
			if site.cert != nil {
				return site.cert, nil
			}
			if site.tlsAuto && mgr != nil {
				return mgr.GetCertificate(hello)
			}
			return nil, fmt.Errorf("no certificate for %q", hello.ServerName)
		},
	}
}
