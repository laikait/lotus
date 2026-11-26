package phpinstall

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ionCube Loader: a Zend extension that runs ionCube-encoded PHP files. It is a separate download from ionCube Ltd,
// which publishes no checksums; the file is fetched over HTTPS from their download host only (no redirect off it),
// capped in size, and PHP is started with it before the change is kept, so a loader that does not work is rolled back.

// IonCubeBase is where ionCube's loader packs are.
var IonCubeBase = "https://downloads.ioncube.com/loader_downloads/"

const maxIonCubeSize = 40 << 20 // the Linux pack holds every PHP version and is about 30 MB

// IonCubeState is what the settings page shows about the loader of one PHP version.
type IonCubeState struct {
	Available  bool   `json:"available"`  // ionCube makes a loader for this PHP version
	Message    string `json:"message"`    // why not, when it is not available
	Downloaded bool   `json:"downloaded"` // the loader file is in the version's folder
	Enabled    bool   `json:"enabled"`    // php.ini loads it
}

var ionLineRe = regexp.MustCompile(`(?i)^\s*zend_extension\s*=\s*"?[^"\n]*ioncube_loader`)

const ionComment = "; ionCube Loader (added by LOTUS; it must be the first zend_extension)"

// ionTarget is the PHP version an operation works on.
type ionTarget struct {
	dir, ini, exe, version string
}

func minorOf(version string) string {
	p := strings.Split(version, ".")
	if len(p) < 2 {
		return ""
	}
	return p[0] + "." + p[1]
}

// ionPack names the pack to download and the loader file inside it, for this system and PHP minor version.
func ionPack(goos, goarch, minor string, zts bool) (pack, entry string, ok bool, why string) {
	switch goos {
	case "windows":
		switch minor {
		case "7.2", "7.3", "7.4":
			pack = "ioncube_loaders_win_vc15_x86-64.zip"
		case "8.1", "8.2", "8.3":
			pack = "ioncube_loaders_win_nonts_vc16_x86-64.zip"
		case "8.4", "8.5":
			pack = "ioncube_loaders_win_nonts_vc17_x86-64.zip"
		default:
			return "", "", false, "ionCube has no Windows loader for PHP " + minor
		}
		return pack, "ioncube/ioncube_loader_win_" + minor + ".dll", true, ""
	case "linux":
		arch := map[string]string{"amd64": "x86-64", "arm64": "aarch64", "386": "x86", "arm": "armv7l"}[goarch]
		if arch == "" {
			return "", "", false, "ionCube has no Linux loader for this processor"
		}
		suffix := ".so"
		if zts {
			suffix = "_ts.so"
		}
		return "ioncube_loaders_lin_" + arch + ".tar.gz", "ioncube/ioncube_loader_lin_" + minor + suffix, true, ""
	}
	return "", "", false, "ionCube loaders are available for Windows and Linux"
}

func (t ionTarget) loaderFile(entry string) string { return filepath.Join(t.dir, path.Base(entry)) }

func readIni(p string) (*iniText, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return parseIni(data), nil
}

func (t *iniText) hasIonCube() bool {
	for _, l := range t.lines {
		if ionLineRe.MatchString(l) {
			return true
		}
	}
	return false
}

// ionState reads the loader's state for a target; entry is the loader file name inside the pack ("" when none exists).
func ionState(t ionTarget, ok bool, why, entry string) (IonCubeState, error) {
	st := IonCubeState{Available: ok, Message: why}
	if !ok {
		return st, nil
	}
	if _, err := os.Stat(t.loaderFile(entry)); err == nil {
		st.Downloaded = true
	}
	ini, err := readIni(t.ini)
	if err != nil {
		return st, err
	}
	st.Enabled = ini.hasIonCube()
	return st, nil
}

// downloadIon fetches a pack and writes the one loader file it holds for this PHP into the version's folder.
func downloadIon(ctx context.Context, client *http.Client, base, pack, entry, dest string, progress Progress) error {
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	full := strings.TrimRight(base, "/") + "/" + pack
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "LOTUS-admin")
	c := client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Minute}
	}
	cc := *c
	cc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 4 || r.URL.Host != u.Host || (r.URL.Scheme != "https" && u.Scheme == "https") {
			return fmt.Errorf("refusing a redirect to %s", r.URL.Redacted())
		}
		return nil
	}
	progress.report(5, "Downloading the ionCube Loader pack")
	resp, err := cc.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the ionCube Loader: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ionCube's download server answered %s", resp.Status)
	}
	if resp.ContentLength > maxIonCubeSize {
		return errors.New("the ionCube download is larger than expected")
	}
	body := &countingReader{r: io.LimitReader(resp.Body, maxIonCubeSize+1), total: resp.ContentLength, progress: progress}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".ioncube-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	var extractErr error
	if strings.HasSuffix(pack, ".zip") {
		extractErr = ionFromZip(body, entry, tmp)
	} else {
		extractErr = ionFromTarGz(body, entry, tmp)
	}
	if cerr := tmp.Close(); extractErr == nil {
		extractErr = cerr
	}
	if extractErr != nil {
		return extractErr
	}
	progress.report(90, "Saving the loader")
	return os.Rename(tmpName, dest)
}

type countingReader struct {
	r        io.Reader
	n, total int64
	progress Progress
	last     int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > maxIonCubeSize {
		return n, errors.New("the ionCube download is larger than expected")
	}
	if c.total > 0 {
		if pct := 5 + int(float64(c.n)/float64(c.total)*80); pct != c.last {
			c.last = pct
			c.progress.report(pct, "Downloading the ionCube Loader pack")
		}
	}
	return n, err
}

// ionFromZip reads a zip held in memory (zip needs random access) and copies the entry out.
func ionFromZip(r io.Reader, entry string, out io.Writer) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytesReaderAt(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("the ionCube pack is not a zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != entry {
			continue
		}
		if f.UncompressedSize64 > maxIonCubeSize {
			return errors.New("the loader file is larger than expected")
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(out, io.LimitReader(src, int64(f.UncompressedSize64)+1))
		return err
	}
	return fmt.Errorf("the ionCube pack has no %s: ionCube may not support this PHP version yet", path.Base(entry))
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func ionFromTarGz(r io.Reader, entry string, out io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("the ionCube pack is not a gzip file: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the ionCube pack: %w", err)
		}
		if h.Name != entry || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > maxIonCubeSize {
			return errors.New("the loader file is larger than expected")
		}
		_, err = io.Copy(out, io.LimitReader(tr, h.Size+1))
		return err
	}
	return fmt.Errorf("the ionCube pack has no %s: ionCube may not support this PHP version yet", path.Base(entry))
}

// setIon adds or removes the zend_extension line and checks that PHP starts with the result; if it does not, the old
// php.ini is put back. The loader is placed first in the file because it must load before other Zend extensions.
func setIon(t ionTarget, loader string, on bool, smoke func(exe, ini string) (string, error)) error {
	old, err := os.ReadFile(t.ini)
	if err != nil {
		return err
	}
	ini := parseIni(old)
	var lines []string
	for _, l := range ini.lines {
		if ionLineRe.MatchString(l) || strings.TrimSpace(l) == ionComment {
			continue
		}
		lines = append(lines, l)
	}
	if on {
		lines = append([]string{ionComment, `zend_extension = "` + filepath.ToSlash(loader) + `"`}, lines...)
	}
	ini.lines = lines
	if err := os.WriteFile(t.ini+".bak", old, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(t.ini, ini.bytes()); err != nil {
		return err
	}
	out, err := smoke(t.exe, t.ini)
	if err == nil && on && !strings.Contains(strings.ToLower(out), "ioncube") {
		err = errors.New("PHP started but did not load the ionCube Loader")
	}
	if err != nil {
		_ = writeFileAtomic(t.ini, old)
		return fmt.Errorf("the ionCube Loader was not enabled, php.ini is unchanged: %v %s", err, strings.TrimSpace(lastLines(out, 3)))
	}
	return nil
}

// defaultIonSmoke runs php-cgi -c <ini> -v and returns what it printed.
func defaultIonSmoke(exe, ini string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "-c", ini, "-v").CombinedOutput()
	return string(out), err
}

// ---- Windows installer ----

func (in *Installer) ionTarget(version string) (ionTarget, error) {
	v, err := in.Get(version)
	if err != nil {
		return ionTarget{}, err
	}
	return ionTarget{dir: v.Dir, ini: v.Ini, exe: v.Exe, version: v.Version}, nil
}

// IonCube reports the ionCube Loader's state for an installed version.
func (in *Installer) IonCube(version string) (IonCubeState, error) {
	t, err := in.ionTarget(version)
	if err != nil {
		return IonCubeState{}, err
	}
	_, entry, ok, why := ionPack(runtime.GOOS, runtime.GOARCH, minorOf(version), false)
	if in.anyOS {
		_, entry, ok, why = ionPack("windows", "amd64", minorOf(version), false)
	}
	return ionState(t, ok, why, entry)
}

// InstallIonCube downloads the loader for a version and enables it.
func (in *Installer) InstallIonCube(ctx context.Context, version string, progress Progress) error {
	t, err := in.ionTarget(version)
	if err != nil {
		return err
	}
	goos := runtime.GOOS
	if in.anyOS {
		goos = "windows"
	}
	pack, entry, ok, why := ionPack(goos, runtime.GOARCH, minorOf(version), false)
	if !ok {
		return errors.New(why)
	}
	base := in.IonCubeBase
	if base == "" {
		base = IonCubeBase
	}
	loader := t.loaderFile(entry)
	if err := downloadIon(ctx, in.Client, base, pack, entry, loader, progress); err != nil {
		return err
	}
	progress.report(95, "Enabling the loader")
	if err := in.setIon(t, loader, true); err != nil {
		_ = os.Remove(loader)
		return err
	}
	progress.report(100, "The ionCube Loader is enabled")
	return nil
}

func (in *Installer) setIon(t ionTarget, loader string, on bool) error {
	smoke := in.IonSmoke
	if smoke == nil {
		smoke = defaultIonSmoke
	}
	return setIon(t, loader, on, smoke)
}

// SetIonCube turns the loader on or off in php.ini; the file must be downloaded to turn it on.
func (in *Installer) SetIonCube(version string, on bool) error {
	t, err := in.ionTarget(version)
	if err != nil {
		return err
	}
	_, entry, ok, why := ionPack(runtime.GOOS, runtime.GOARCH, minorOf(version), false)
	if in.anyOS {
		_, entry, ok, why = ionPack("windows", "amd64", minorOf(version), false)
	}
	if !ok {
		return errors.New(why)
	}
	loader := t.loaderFile(entry)
	if on {
		if _, err := os.Stat(loader); err != nil {
			return errors.New("the ionCube Loader is not downloaded yet")
		}
	}
	return in.setIon(t, loader, on)
}

// ---- Linux manager ----

func (m *LinuxManager) ionTarget(version string) (ionTarget, bool, error) {
	v, err := m.Get(version)
	if err != nil {
		return ionTarget{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := m.run(ctx, nil, v.Exe, "-v")
	return ionTarget{dir: v.Dir, ini: v.Ini, exe: v.Exe, version: v.Version}, strings.Contains(string(out), "ZTS"), nil
}

func (m *LinuxManager) goos() string {
	if m.anyOS {
		return "linux"
	}
	return runtime.GOOS
}

// IonCube reports the ionCube Loader's state for a version in use.
func (m *LinuxManager) IonCube(version string) (IonCubeState, error) {
	t, zts, err := m.ionTarget(version)
	if err != nil {
		return IonCubeState{}, err
	}
	_, entry, ok, why := ionPack(m.goos(), runtime.GOARCH, minorOf(version), zts)
	if !ok && m.anyOS {
		_, entry, ok, why = ionPack("linux", "amd64", minorOf(version), zts)
	}
	return ionState(t, ok, why, entry)
}

// InstallIonCube downloads the loader for a version and enables it.
func (m *LinuxManager) InstallIonCube(ctx context.Context, version string, progress Progress) error {
	t, zts, err := m.ionTarget(version)
	if err != nil {
		return err
	}
	arch := runtime.GOARCH
	if m.anyOS {
		arch = "amd64"
	}
	pack, entry, ok, why := ionPack(m.goos(), arch, minorOf(version), zts)
	if !ok {
		return errors.New(why)
	}
	base := m.IonCubeBase
	if base == "" {
		base = IonCubeBase
	}
	loader := t.loaderFile(entry)
	if err := downloadIon(ctx, nil, base, pack, entry, loader, progress); err != nil {
		return err
	}
	progress.report(95, "Enabling the loader")
	if err := m.setIon(t, loader, true); err != nil {
		_ = os.Remove(loader)
		return err
	}
	progress.report(100, "The ionCube Loader is enabled")
	return nil
}

func (m *LinuxManager) setIon(t ionTarget, loader string, on bool) error {
	smoke := m.IonSmoke
	if smoke == nil {
		smoke = defaultIonSmoke
	}
	return setIon(t, loader, on, smoke)
}

// SetIonCube turns the loader on or off in the version's php.ini.
func (m *LinuxManager) SetIonCube(version string, on bool) error {
	t, zts, err := m.ionTarget(version)
	if err != nil {
		return err
	}
	arch := runtime.GOARCH
	if m.anyOS {
		arch = "amd64"
	}
	_, entry, ok, why := ionPack(m.goos(), arch, minorOf(version), zts)
	if !ok {
		return errors.New(why)
	}
	loader := t.loaderFile(entry)
	if on {
		if _, err := os.Stat(loader); err != nil {
			return errors.New("the ionCube Loader is not downloaded yet")
		}
	}
	return m.setIon(t, loader, on)
}
