// Package phpinstall downloads, installs, configures and removes PHP for Windows, for the web admin's PHP Versions
// page (docs/architecture/admin.md). Builds are the official NTS zips of windows.php.net: the list comes from its
// releases.json, and every zip is checked against the SHA-256 given there before anything is unpacked.
//
// The install folder is fixed by the caller (Installer.Home); nothing in this package takes a path from a request.
package phpinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is where the official Windows builds and their releases.json live.
const DefaultBaseURL = "https://windows.php.net/downloads/releases/"

// Limits.
const (
	maxFeedSize     = 2 << 20   // releases.json is about 30 KB
	defaultMaxZip   = 150 << 20 // a PHP zip is about 35 MB
	maxUnpackedSize = 700 << 20
	maxFiles        = 20000
	feedCacheTime   = 10 * time.Minute
)

// maxZipSize is the largest download kept; a variable so that a test can lower it.
var maxZipSize int64 = defaultMaxZip

// Errors the callers tell apart.
var (
	ErrUnsupported    = errors.New("installing PHP from the panel is only available on Windows")
	ErrBusy           = errors.New("another PHP install is running")
	ErrUnknownVersion = errors.New("this PHP version is not offered by windows.php.net")
	ErrInstalled      = errors.New("this PHP version is already installed")
	ErrNotInstalled   = errors.New("this PHP version is not installed")
)

var versionRe = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,3}$`)

// ValidVersion reports whether v looks like an x.y.z version.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Release is one version the feed offers: the NTS x64 zip of a branch.
type Release struct {
	Branch  string `json:"branch"`  // 8.3
	Version string `json:"version"` // 8.3.37
	Path    string `json:"-"`       // file name under the base URL
	SHA256  string `json:"-"`
	Size    int64  `json:"size"` // bytes, from the feed's "37.82MB" (approximate)
	Found   string `json:"-"`    // Linux: the php-cgi program already on the system (use it, no install)
}

// Installer works on one PHP home folder.
type Installer struct {
	Home    string // the folder that holds one folder per installed version
	BaseURL string // DefaultBaseURL when empty; tests point it at a local server
	Client  *http.Client

	// Runtime checks that the system can run PHP (the Visual C++ runtime). Nil: RuntimeCheck.
	Runtime func() error
	// IonCubeBase is where ionCube's packs are (IonCubeBase when empty); IonSmoke starts PHP with a php.ini to see that
	// the loader works (nil: run php-cgi -c INI -v). Tests set both.
	IonCubeBase string
	IonSmoke    func(exe, ini string) (string, error)

	// Smoke runs a freshly unpacked php-cgi to see that it starts. Nil: run it with -v.
	Smoke func(exe string) error

	insecure bool // tests: allow http://
	anyOS    bool // tests: allow installing on a system other than Windows

	mu      sync.Mutex // guards the feed cache
	feed    []Release
	feedAt  time.Time
	busy    sync.Mutex // one install at a time
	nowFunc func() time.Time
}

func (in *Installer) now() time.Time {
	if in.nowFunc != nil {
		return in.nowFunc()
	}
	return time.Now()
}

func (in *Installer) base() string {
	if in.BaseURL != "" {
		return strings.TrimRight(in.BaseURL, "/") + "/"
	}
	return DefaultBaseURL
}

// Supported says whether installs work on this system.
func (in *Installer) Supported() bool { return runtime.GOOS == "windows" || in.anyOS }

// hostAllowed says whether a download may come from host: the feed's own host, and for the real feed also
// downloads.php.net, where windows.php.net sends its files (both are the PHP project's servers).
func (in *Installer) hostAllowed(host string) bool {
	base, err := url.Parse(in.base())
	if err != nil {
		return false
	}
	if host == base.Host {
		return true
	}
	return base.Host == "windows.php.net" && host == "downloads.php.net"
}

// client returns the HTTP client: only the feed's host, no downgrade to http, a bounded number of redirects.
func (in *Installer) client() *http.Client {
	if in.Client != nil {
		return in.Client
	}
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("too many redirects")
			}
			if !in.hostAllowed(req.URL.Host) || (req.URL.Scheme != "https" && !in.insecure) {
				return fmt.Errorf("refusing a redirect to %s", req.URL.Redacted())
			}
			return nil
		},
	}
}

func (in *Installer) get(ctx context.Context, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if !in.hostAllowed(u.Host) || (u.Scheme != "https" && !in.insecure) {
		return nil, fmt.Errorf("refusing to fetch %s", u.Redacted())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "LOTUS-admin")
	resp, err := in.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("windows.php.net answered %s", resp.Status)
	}
	return resp, nil
}

// Releases returns the versions windows.php.net offers (newest branch first), cached for ten minutes.
func (in *Installer) Releases(ctx context.Context) ([]Release, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.feed != nil && in.now().Sub(in.feedAt) < feedCacheTime {
		return in.feed, nil
	}
	resp, err := in.get(ctx, in.base()+"releases.json")
	if err != nil {
		return nil, fmt.Errorf("reading the list of PHP versions: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading the list of PHP versions: %w", err)
	}
	if len(data) > maxFeedSize {
		return nil, errors.New("the list of PHP versions is too big")
	}
	rel, err := ParseReleases(data)
	if err != nil {
		return nil, err
	}
	in.feed, in.feedAt = rel, in.now()
	return rel, nil
}

var (
	ntsKeyRe = regexp.MustCompile(`^nts-v[cs][0-9]+-x64$`)
	shaRe    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	zipRe    = regexp.MustCompile(`^php-[0-9.]+-nts-Win32-v[cs][0-9]+-x64\.zip$`)
)

// ParseReleases reads releases.json: for each branch the newest version and its NTS x64 zip. A branch without
// such a zip, or with a name or hash that does not look right, is left out.
func ParseReleases(data []byte) ([]Release, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the list of PHP versions is not valid JSON: %w", err)
	}
	var out []Release
	for branch, msg := range raw {
		var b map[string]json.RawMessage
		if json.Unmarshal(msg, &b) != nil {
			continue
		}
		var version string
		if json.Unmarshal(b["version"], &version) != nil || !versionRe.MatchString(version) {
			continue
		}
		for key, v := range b {
			if !ntsKeyRe.MatchString(key) {
				continue
			}
			var build struct {
				Zip struct {
					Path   string `json:"path"`
					Size   string `json:"size"`
					SHA256 string `json:"sha256"`
				} `json:"zip"`
			}
			if json.Unmarshal(v, &build) != nil || !zipRe.MatchString(build.Zip.Path) || !shaRe.MatchString(build.Zip.SHA256) {
				continue
			}
			if !strings.HasPrefix(build.Zip.Path, "php-"+version+"-") {
				continue // the file must be the one for that version
			}
			out = append(out, Release{Branch: branch, Version: version, Path: build.Zip.Path, SHA256: strings.ToLower(build.Zip.SHA256), Size: parseSize(build.Zip.Size)})
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the list of PHP versions has no usable entry")
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}

// parseSize turns "37.82MB" into bytes (megabytes of 1024*1024). It is for a progress bar only; 0 when unknown.
func parseSize(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	default:
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 || f > 10000 {
		return 0
	}
	return int64(f * mult)
}

// versionLess orders x.y.z versions numerically.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}
