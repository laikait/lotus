package server

import (
	"net/http"
	"reflect"

	"github.com/laikait/lotus/internal/acme"
	"github.com/laikait/lotus/internal/config/model"
)

// Automatic certificates (docs/architecture/acme.md).
//
// The manager belongs to the Server, not to a generation: certificates and
// renewals outlive a reload. A reload may add or remove sites that use
// `tls automatic`, but can't change how certificates are obtained (the CA,
// the account, the storage): that needs a restart.

// acmeRuntime holds the manager and the settings it was made from.
type acmeRuntime struct {
	mgr *acme.Manager
	cfg model.ACME
}

// usesACME says whether any site asks for automatic certificates.
func usesACME(cfg *model.Config) bool {
	for i := range cfg.Sites {
		if cfg.Sites[i].TLS == model.TLSAutomatic {
			return true
		}
	}
	return false
}

// acmeNames lists the names of the sites that use automatic certificates.
func acmeNames(cfg *model.Config) []string {
	var out []string
	for i := range cfg.Sites {
		s := &cfg.Sites[i]
		if s.TLS == model.TLSAutomatic {
			out = append(out, s.Name)
			out = append(out, s.Aliases...)
		}
	}
	return out
}

// acmeManager returns the running manager, or nil.
func (s *Server) acmeManager() *acme.Manager {
	if r := s.acmeRT.Load(); r != nil {
		return r.mgr
	}
	return nil
}

// setupACME creates the manager if the configuration needs one. It does not
// contact the CA.
func (s *Server) setupACME(cfg *model.Config) error {
	if !usesACME(cfg) || cfg.TLS.ACME == nil {
		return nil
	}
	a := *cfg.TLS.ACME
	m, err := acme.New(acme.Config{
		Email: a.Email, CA: a.CA, CARoot: a.CARoot,
		EABKeyID: a.EABKeyID, EABHMACKey: string(a.EABHMACKey),
		Storage: a.Storage, HTTPPort: a.HTTPPort, TLSALPNPort: a.TLSALPNPort, Challenges: a.Challenges,
		Logger: s.log,
	})
	if err != nil {
		return err
	}
	s.acmeRT.Store(&acmeRuntime{mgr: m, cfg: a})
	return nil
}

// manageCertificates starts obtaining and renewing the certificates of the
// configuration's automatic sites. It is called once the listeners are up
// (the CA connects to them to check the challenges) and after each reload.
func (s *Server) manageCertificates(cfg *model.Config) {
	m := s.acmeManager()
	if m == nil {
		return
	}
	for name, why := range m.Manage(acmeNames(cfg)) {
		s.log.Warn("no automatic certificate for this name", "name", name, "reason", why)
	}
}

// acmeChanged lists what differs between the running manager and cfg that a
// reload can't apply. It sets up a manager when there was none.
func (s *Server) acmeRestartRequired(cfg *model.Config) []string {
	cur := s.acmeRT.Load()
	if !usesACME(cfg) || cfg.TLS.ACME == nil {
		return nil
	}
	if cur == nil {
		return nil // set up by the reload itself
	}
	if !reflect.DeepEqual(cur.cfg, *cfg.TLS.ACME) {
		return []string{"acme settings changed (CA, account, storage or ports)"}
	}
	return nil
}

// handleACMEChallenge answers an HTTP-01 validation, if r is one.
func (s *Server) handleACMEChallenge(w http.ResponseWriter, r *http.Request) bool {
	m := s.acmeManager()
	if m == nil || !acme.LooksLikeHTTPChallenge(r) {
		return false
	}
	return m.HandleHTTPChallenge(w, r)
}

func (s *Server) closeACME() {
	if r := s.acmeRT.Swap(nil); r != nil {
		r.mgr.Close()
	}
}
