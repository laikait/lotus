package server

import (
	"context"
	"crypto/x509"
	"errors"
	"sort"
	"time"

	"github.com/laikait/lotus/internal/acme"
	"github.com/laikait/lotus/internal/config/model"
)

// CertInfo describes a certificate the server uses or is waiting for.
type CertInfo struct {
	Name     string   // the site name (tls manual) or the managed name (tls automatic)
	Site     string   // the site, for a certificate from a file
	Source   string   // "file" or "acme"
	Domains  []string // the names the certificate covers
	Issuer   string
	NotAfter time.Time // zero while Ready is false
	Ready    bool      // false for a certificate that is still to be obtained
}

// ErrNoACME means the running configuration has no automatic certificates, so there is nothing to renew.
var ErrNoACME = errors.New("the running configuration has no automatic certificates")

// RenewCertificate gets a new certificate for a name with `tls automatic` now (or the first one, if it has none yet).
// acme.ErrNotManaged and acme.ErrRenewedRecently say why nothing was done.
func (s *Server) RenewCertificate(ctx context.Context, name string) error {
	m := s.acmeManager()
	if m == nil {
		return ErrNoACME
	}
	return m.Renew(ctx, name)
}

// Certificates lists the certificates of the running configuration: the ones
// loaded from files (tls manual), and one entry for each name that is obtained
// from a CA (tls automatic), ready or not. Sorted by name.
func (s *Server) Certificates() []CertInfo {
	g := s.gen.Load()
	var out []CertInfo
	for i := range g.cfg.Sites {
		site := &g.cfg.Sites[i]
		if site.TLS != model.TLSManual {
			continue
		}
		rt := g.sites[site.Name]
		if rt == nil || rt.cert == nil || rt.cert.Leaf == nil {
			continue
		}
		leaf := rt.cert.Leaf
		out = append(out, CertInfo{
			Name: site.Name, Site: site.Name, Source: "file",
			Domains: certNames(leaf), Issuer: issuerOf(leaf), NotAfter: leaf.NotAfter, Ready: true,
		})
	}
	if m := s.acmeManager(); m != nil {
		for _, c := range m.Certificates() {
			out = append(out, CertInfo{Name: c.Name, Source: "acme", Domains: c.Names, Issuer: c.Issuer, NotAfter: c.NotAfter, Ready: c.Ready})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func certNames(c *x509.Certificate) []string {
	names := append([]string(nil), c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = []string{c.Subject.CommonName}
	}
	return names
}

func issuerOf(c *x509.Certificate) string { return acme.IssuerName(c) }
