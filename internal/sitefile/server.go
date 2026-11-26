package sitefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/model"
)

// The managed server file holds the server-level settings that the admin may change: PHP pools and the HTTPS
// wizard. It is sites.d/00-lotus-server.conf (the 00 makes it load first), written the same way as a site file: a
// marker, a spec line, and text that must equal what is rendered from the spec, or the file counts as hand-written.

// ServerFile is the name (without .conf) of the managed server file.
const ServerFile = "00-lotus-server"

// ServerMarker is the first line of the managed server file.
const ServerMarker = "# Managed by LOTUS (the web admin: PHP pools, HTTPS). Keep this line and the layout, or the admin shows it read-only."

// StagingCA is Let's Encrypt's test CA. Its certificates are not trusted by browsers, but its limits are generous.
const StagingCA = "https://acme-staging-v02.api.letsencrypt.org/directory"

// PHPPoolSpec is one php_backend that LOTUS starts php-cgi processes for.
type PHPPoolSpec struct {
	Name        string `json:"name"`
	Command     string `json:"command"`                // a path, or a bare name looked up in PATH
	Processes   int    `json:"processes"`              // 1 to 64
	MaxRequests int    `json:"max_requests,omitempty"` // 0: never replace a process
	PHPIni      string `json:"php_ini,omitempty"`
	Timeout     int    `json:"timeout"` // seconds, 1 to 600
}

// HTTPSSpec is what the HTTPS wizard writes: automatic certificates and the listeners they need.
type HTTPSSpec struct {
	Email   string `json:"email"`
	HTTPS   string `json:"https"`          // address of the HTTPS listener, such as 0.0.0.0:443
	HTTP    string `json:"http,omitempty"` // address of a plain HTTP listener (HTTP-01 challenges); "" for none
	Staging bool   `json:"staging"`        // use the test CA
}

// ServerSpec is everything the managed server file says.
type ServerSpec struct {
	PHP   []PHPPoolSpec `json:"php,omitempty"`
	HTTPS *HTTPSSpec    `json:"https,omitempty"`
}

var (
	emailRe   = regexp.MustCompile(`^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9.-]{1,190}\.[A-Za-z]{2,}$`)
	commandRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9.-]*$|^\[[0-9A-Fa-f:]+\]$`)
)

// Names of the listeners the wizard makes. Another listener with the same name makes the check fail, as it should.
const (
	HTTPSListener = "lotus-https"
	HTTPListener  = "lotus-http"
)

// NormalizeServer trims the text fields and fills the defaults.
func NormalizeServer(sp ServerSpec) ServerSpec {
	for i := range sp.PHP {
		p := &sp.PHP[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Command = strings.ReplaceAll(strings.TrimSpace(p.Command), `\`, "/")
		p.PHPIni = strings.ReplaceAll(strings.TrimSpace(p.PHPIni), `\`, "/")
		if p.Timeout == 0 {
			p.Timeout = 60
		}
	}
	if sp.HTTPS != nil {
		h := *sp.HTTPS
		h.Email = strings.TrimSpace(h.Email)
		h.HTTPS = strings.TrimSpace(h.HTTPS)
		h.HTTP = strings.TrimSpace(h.HTTP)
		if h.HTTPS == "" {
			h.HTTPS = "0.0.0.0:443"
		}
		sp.HTTPS = &h
	}
	return sp
}

func checkAddress(a string) error {
	host, port, err := net.SplitHostPort(a)
	if err != nil || !hostRe.MatchString(host) {
		return fmt.Errorf("address %q must look like 0.0.0.0:443 or [::]:443", a)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("address %q: the port must be 1 to 65535", a)
	}
	return nil
}

// ValidateServer checks a normalized ServerSpec. The values end up in a file, so each has a strict pattern.
func ValidateServer(sp ServerSpec) error {
	if len(sp.PHP) > 20 {
		return errors.New("at most 20 PHP pools")
	}
	seen := map[string]bool{}
	for _, p := range sp.PHP {
		if !backendRe.MatchString(p.Name) {
			return fmt.Errorf("pool name %q: use letters, digits, _ and - only", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("pool %q is listed twice", p.Name)
		}
		seen[p.Name] = true
		if strings.Contains(p.Command, "/") {
			if err := checkPath(p.Command); err != nil {
				return fmt.Errorf("pool %q, php-cgi program: %w", p.Name, err)
			}
		} else if !commandRe.MatchString(p.Command) {
			return fmt.Errorf("pool %q: the php-cgi program must be a full path (C:/xampp/php/php-cgi.exe) or a name found in PATH (php-cgi)", p.Name)
		}
		if p.Processes < 1 || p.Processes > 64 {
			return fmt.Errorf("pool %q: processes must be 1 to 64", p.Name)
		}
		if p.MaxRequests < 0 || p.MaxRequests > 1000000 {
			return fmt.Errorf("pool %q: max requests must be 0 to 1000000", p.Name)
		}
		if p.Timeout < 1 || p.Timeout > 600 {
			return fmt.Errorf("pool %q: the timeout must be 1 to 600 seconds", p.Name)
		}
		if p.PHPIni != "" {
			if err := checkPath(p.PHPIni); err != nil {
				return fmt.Errorf("pool %q, php.ini: %w", p.Name, err)
			}
		}
	}
	if h := sp.HTTPS; h != nil {
		if len(h.Email) > 254 || !emailRe.MatchString(h.Email) {
			return fmt.Errorf("%q is not an e-mail address (the certificate authority sends expiry warnings to it)", h.Email)
		}
		if err := checkAddress(h.HTTPS); err != nil {
			return err
		}
		if h.HTTP != "" {
			if err := checkAddress(h.HTTP); err != nil {
				return err
			}
			if h.HTTP == h.HTTPS {
				return errors.New("the HTTP and HTTPS listeners can't use the same address")
			}
		}
	}
	return nil
}

// RenderServer is the text of the managed server file for a normalized, valid spec.
func RenderServer(sp ServerSpec) string {
	var b strings.Builder
	b.WriteString(ServerMarker + "\n")
	js, _ := json.Marshal(sp)
	b.WriteString(SpecPrefix + string(js) + "\n")
	for _, p := range sp.PHP {
		fmt.Fprintf(&b, "\nphp_backend %q {\n    command %q\n    processes %d\n", p.Name, p.Command, p.Processes)
		if p.MaxRequests > 0 {
			fmt.Fprintf(&b, "    max_requests %d\n", p.MaxRequests)
		}
		if p.PHPIni != "" {
			fmt.Fprintf(&b, "    php_ini %q\n", p.PHPIni)
		}
		fmt.Fprintf(&b, "    timeout %ds\n}\n", p.Timeout)
	}
	if h := sp.HTTPS; h != nil {
		if h.HTTP != "" {
			fmt.Fprintf(&b, "\nlisten %q {\n    address %q\n}\n", HTTPListener, h.HTTP)
		}
		fmt.Fprintf(&b, "\nlisten %q {\n    address %q\n    protocol https\n}\n", HTTPSListener, h.HTTPS)
		fmt.Fprintf(&b, "\ntls {\n    acme {\n        email %q\n", h.Email)
		if h.Staging {
			fmt.Fprintf(&b, "        ca %q\n", StagingCA)
		}
		b.WriteString("    }\n}\n")
	}
	return b.String()
}

// parseServer reads the managed server file. ok is false if it was changed by hand.
func parseServer(text string) (ServerSpec, bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 2 || lines[0] != ServerMarker || !strings.HasPrefix(lines[1], SpecPrefix) {
		return ServerSpec{}, false
	}
	var sp ServerSpec
	dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(lines[1], SpecPrefix)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sp); err != nil {
		return ServerSpec{}, false
	}
	sp = NormalizeServer(sp)
	if ValidateServer(sp) != nil || RenderServer(sp) != text {
		return ServerSpec{}, false
	}
	return sp, true
}

// ErrServerFileByHand means the server file exists but was changed by hand, so it can't be changed here.
var ErrServerFileByHand = errors.New("the server file sites.d/00-lotus-server.conf was changed by hand, so it can't be changed here")

// Server returns what the managed server file says (an empty spec if there is none). editable is false when the file
// exists but was changed by hand.
func (s *Store) Server() (sp ServerSpec, editable bool, err error) {
	data, err := os.ReadFile(s.File(ServerFile))
	if errors.Is(err, os.ErrNotExist) {
		return ServerSpec{}, true, nil
	}
	if err != nil {
		return ServerSpec{}, false, err
	}
	sp, ok := parseServer(string(data))
	return sp, ok, nil
}

// SaveServer writes the managed server file for sp, or removes it when sp says nothing. The whole configuration is
// checked and the old file put back if it is not valid. It returns the old text for Restore.
func (s *Store) SaveServer(sp ServerSpec) (old []byte, err error) {
	sp = NormalizeServer(sp)
	if err := ValidateServer(sp); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.File(ServerFile)
	old, err = os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if _, ok := parseServer(string(old)); !ok {
			return nil, ErrServerFileByHand
		}
	} else {
		old = nil
	}
	if len(sp.PHP) == 0 && sp.HTTPS == nil {
		if old == nil {
			return nil, nil
		}
		if err := os.Remove(file); err != nil {
			return nil, err
		}
		if res, diags := config.Check(s.Config, loader.Options{}); res == nil { // removing a pool can break a site that uses it
			_ = os.WriteFile(file, old, 0o644)
			return nil, &InvalidError{Diagnostics: diags}
		}
		return old, nil
	}
	return old, s.write(file, RenderServer(sp), old)
}

// Running describes what the configuration files say now (not what the server runs): the listeners and the ACME
// e-mail, for the HTTPS wizard. ok is false when the files are not valid.
func (s *Store) Running() (listeners []model.Listener, acmeEmail string, hasACME, ok bool) {
	res, _ := config.Check(s.Config, loader.Options{})
	if res == nil || res.Config == nil {
		return nil, "", false, false
	}
	if a := res.Config.TLS.ACME; a != nil {
		acmeEmail, hasACME = a.Email, true
	}
	return res.Config.Listeners, acmeEmail, hasACME, true
}

// ListenerNames lists the listener names of the current configuration files, for the site form.
func (s *Store) ListenerNames() []string {
	ls, _, _, ok := s.Running()
	names := []string{}
	if !ok {
		return names
	}
	for _, l := range ls {
		names = append(names, l.Name)
	}
	return names
}

// PHPBackendCommands maps each php_backend of the configuration files that LOTUS starts itself to its program path.
func (s *Store) PHPBackendCommands() map[string]string {
	out := map[string]string{}
	res, _ := config.Check(s.Config, loader.Options{})
	if res == nil || res.Config == nil {
		return out
	}
	for _, b := range res.Config.PHPBackends {
		if b.Command != "" {
			out[b.Name] = b.Command
		}
	}
	return out
}
