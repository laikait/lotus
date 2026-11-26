package sitefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
)

// Templates are the kinds of site the admin can make. Each one produces its routes from the structured fields of
// the Spec, never from typed text.
var Templates = []string{"static", "php", "wordpress", "laravel", "proxy"}

// SpecPrefix starts the second line of a site file: the Spec as JSON. A file is editable only when its whole text is
// exactly what Render makes from that Spec.
const SpecPrefix = "# spec: "

var (
	backendRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	proxyRe    = regexp.MustCompile(`^https?://[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^https?://\[[0-9A-Fa-f:]+\](:[0-9]{1,5})?$`)
	statusRe   = regexp.MustCompile(`^[45][0-9][0-9]$`)
	errorURLRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,200}$`)
)

func isPHP(t string) bool { return t == "php" || t == "wordpress" || t == "laravel" }

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// normalizeSettings fills defaults and drops fields that do not belong to the template, so a spec has one text.
func normalizeSettings(sp *Spec) {
	sp.Template = strings.ToLower(strings.TrimSpace(sp.Template))
	if sp.Template == "" {
		sp.Template = "static"
	}
	sp.PHPBackend = strings.TrimSpace(sp.PHPBackend)
	sp.ProxyURL = strings.TrimRight(strings.TrimSpace(sp.ProxyURL), "/")
	if !isPHP(sp.Template) {
		sp.PHPBackend = ""
	}
	if sp.Template != "proxy" {
		sp.ProxyURL = ""
	} else { // files are not served, so these settings would do nothing
		sp.ErrorPages, sp.Symlinks, sp.Dotfiles, sp.HTAccess = nil, "", "", false
	}
	if isPHP(sp.Template) && len(sp.Index) == 0 {
		sp.Index = []string{"index.php", "index.html"}
	}
	if sp.RateRequests <= 0 {
		sp.RateRequests, sp.RatePer, sp.RateBurst = 0, "", 0
	} else if sp.RatePer == "" {
		sp.RatePer = "1s"
	}
	if sp.RateBurst < 0 {
		sp.RateBurst = 0
	}
	sp.AccessLog = strings.ReplaceAll(strings.TrimSpace(sp.AccessLog), `\`, "/")
	sp.ErrorLog = strings.ReplaceAll(strings.TrimSpace(sp.ErrorLog), `\`, "/")
	for k, v := range sp.ErrorPages {
		v = strings.TrimSpace(v)
		if v == "" {
			delete(sp.ErrorPages, k)
			continue
		}
		sp.ErrorPages[strings.TrimSpace(k)] = v
	}
	if len(sp.ErrorPages) == 0 {
		sp.ErrorPages = nil
	}
}

// validateSettings checks the fields added in spec version 2. The values end up in a file, so each is an enum or a
// strict pattern.
func validateSettings(sp Spec) error {
	if !oneOf(sp.Template, Templates...) {
		return fmt.Errorf("template must be one of %s", strings.Join(Templates, ", "))
	}
	if isPHP(sp.Template) {
		if sp.PHPBackend == "" {
			return errors.New("choose a PHP backend for this template")
		}
		if !backendRe.MatchString(sp.PHPBackend) {
			return fmt.Errorf("PHP backend %q: use letters, digits, _ and - only", sp.PHPBackend)
		}
	}
	if sp.Template == "proxy" {
		if !proxyRe.MatchString(sp.ProxyURL) {
			return fmt.Errorf("proxy address %q must look like http://127.0.0.1:3000 or https://app.internal:8443 (no path, no user name)", sp.ProxyURL)
		}
	}
	if sp.RateRequests > 0 {
		if sp.RateRequests > 100000 || sp.RateBurst > 100000 {
			return errors.New("the rate limit may not be above 100000")
		}
		if !oneOf(sp.RatePer, "1s", "1m", "1h") {
			return errors.New(`the rate limit period must be "1s", "1m" or "1h"`)
		}
	}
	if !oneOf(sp.Compression, "", "on", "off") {
		return errors.New(`compression must be "", "on" or "off"`)
	}
	if !oneOf(sp.SecurityHeaders, "", "basic") {
		return errors.New(`security headers must be "" or "basic"`)
	}
	if !oneOf(sp.Symlinks, "", "follow", "owner_match", "deny") {
		return errors.New(`symlinks must be "", "follow", "owner_match" or "deny"`)
	}
	if !oneOf(sp.Dotfiles, "", "deny", "allow") {
		return errors.New(`dotfiles must be "", "deny" or "allow"`)
	}
	if len(sp.ErrorPages) > 10 {
		return errors.New("at most 10 error pages")
	}
	for k, v := range sp.ErrorPages {
		if !statusRe.MatchString(k) {
			return fmt.Errorf("error page status %q must be a 4xx or 5xx number", k)
		}
		if !errorURLRe.MatchString(v) || strings.Contains(v, "..") {
			return fmt.Errorf("error page %q: use a path such as /404.html", v)
		}
	}
	for _, p := range []string{sp.AccessLog, sp.ErrorLog} {
		if p != "" {
			if err := checkPath(p); err != nil {
				return fmt.Errorf("log file: %w", err)
			}
		}
	}
	return nil
}

// DocRoot is the folder the site serves files from: the web directory, or its public folder for Laravel.
func DocRoot(sp Spec) string {
	if sp.Template == "laravel" {
		return strings.TrimRight(sp.Root, "/") + "/public"
	}
	return sp.Root
}

// UpstreamName is the name of the upstream a proxy site gets.
func UpstreamName(site string) string { return "site-" + site }

// render is the text of a site file for a normalized, valid spec.
func render(sp Spec) string {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	js, _ := json.Marshal(sp)
	b.WriteString(SpecPrefix + string(js) + "\n")
	if sp.Template == "proxy" {
		fmt.Fprintf(&b, "upstream %q {\n    backend %q\n}\n\n", UpstreamName(sp.Name), sp.ProxyURL)
	}
	fmt.Fprintf(&b, "site %q {\n", sp.Name)
	if len(sp.Aliases) > 0 {
		fmt.Fprintf(&b, "    aliases %s\n", quoteList(sp.Aliases))
	}
	if sp.Template != "proxy" { // a proxy site serves no files, so it has no root (and none that must exist)
		fmt.Fprintf(&b, "    root %q\n", DocRoot(sp))
		fmt.Fprintf(&b, "    index %s\n", quoteList(sp.Index))
	}
	fmt.Fprintf(&b, "    tls %s\n", sp.TLS)
	if sp.Symlinks != "" {
		fmt.Fprintf(&b, "    symlinks %s\n", sp.Symlinks)
	}
	if sp.Dotfiles != "" {
		fmt.Fprintf(&b, "    dotfiles %s\n", sp.Dotfiles)
	}
	if sp.HTAccess {
		b.WriteString("    htaccess true\n")
	}
	switch sp.Compression {
	case "on":
		b.WriteString("    compression {\n        algorithms [ \"br\", \"zstd\", \"gzip\" ]\n    }\n")
	case "off":
		b.WriteString("    compression { enabled false }\n")
	}
	if sp.SecurityHeaders == "basic" {
		b.WriteString("    security_headers {\n" +
			"        \"X-Content-Type-Options\" = \"nosniff\"\n" +
			"        \"X-Frame-Options\" = \"SAMEORIGIN\"\n" +
			"        \"Referrer-Policy\" = \"strict-origin-when-cross-origin\"\n" +
			"    }\n")
	}
	if sp.RateRequests > 0 {
		fmt.Fprintf(&b, "    rate_limit {\n        requests %d\n        per %s\n", sp.RateRequests, sp.RatePer)
		if sp.RateBurst > 0 {
			fmt.Fprintf(&b, "        burst %d\n", sp.RateBurst)
		}
		b.WriteString("    }\n")
	}
	if sp.AccessLog != "" || sp.ErrorLog != "" {
		b.WriteString("    logs {\n")
		if sp.AccessLog != "" {
			fmt.Fprintf(&b, "        access %q\n", sp.AccessLog)
		}
		if sp.ErrorLog != "" {
			fmt.Fprintf(&b, "        error %q\n", sp.ErrorLog)
		}
		b.WriteString("    }\n")
	}
	codes := make([]string, 0, len(sp.ErrorPages))
	for k := range sp.ErrorPages {
		codes = append(codes, k)
	}
	sort.Strings(codes)
	for _, k := range codes {
		fmt.Fprintf(&b, "    error_page %s %q\n", k, sp.ErrorPages[k])
	}
	switch {
	case isPHP(sp.Template):
		uploads := "/uploads/"
		if sp.Template == "wordpress" {
			uploads = "/wp-content/uploads/"
		}
		fmt.Fprintf(&b, "\n    route ~ '\\.php(/|$)' {\n        php %q\n    }\n", sp.PHPBackend)
		fmt.Fprintf(&b, "\n    route ^~ %q {\n        php_deny true\n    }\n", uploads)
		b.WriteString("\n    route \"/\" {\n        try_files [ \"{uri}\", \"{uri}/\", \"/index.php?{query}\" ]\n    }\n")
	case sp.Template == "proxy":
		fmt.Fprintf(&b, "\n    route \"/\" {\n        proxy %q\n    }\n", UpstreamName(sp.Name))
	}
	b.WriteString("}\n")
	return b.String()
}

// parseV2 reads a file whose text is exactly what render makes from the spec on its second line.
func parseV2(text string) (Spec, bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 3 || lines[0] != Marker || !strings.HasPrefix(lines[1], SpecPrefix) {
		return Spec{}, false
	}
	var sp Spec
	dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(lines[1], SpecPrefix)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sp); err != nil {
		return Spec{}, false
	}
	sp = Normalize(sp)
	if Validate(sp) != nil || render(sp) != text {
		return Spec{}, false
	}
	return sp, true
}

// PHPBackendNames lists the php_backend names of the current configuration, for the template form.
func (s *Store) PHPBackendNames() []string {
	res, _ := config.Check(s.Config, loader.Options{})
	if res == nil || res.Config == nil {
		return []string{}
	}
	names := make([]string, 0, len(res.Config.PHPBackends))
	for _, b := range res.Config.PHPBackends {
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}
