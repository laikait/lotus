package phpinstall

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ExeName is the FastCGI program inside an installed version.
const ExeName = "php-cgi.exe"

// metaFile marks a folder as made by this package. Remove deletes only such folders.
const metaFile = "lotus-php.json"

// Meta is what is kept next to an installed version.
type Meta struct {
	Version     string    `json:"version"`
	Source      string    `json:"source"`
	SHA256      string    `json:"sha256"`
	InstalledAt time.Time `json:"installed_at"`
	Exe         string    `json:"exe,omitempty"` // Linux: the system's php-cgi this version uses
}

// Installed is a version found in the PHP home.
type Installed struct {
	Version     string    `json:"version"`
	Dir         string    `json:"dir"`
	Exe         string    `json:"exe"`
	Ini         string    `json:"ini"`
	InstalledAt time.Time `json:"installed_at"`
}

// Progress is called while an install runs: a percentage (0 to 100) and a short sentence.
type Progress func(percent int, message string)

func (p Progress) report(percent int, msg string) {
	if p != nil {
		p(percent, msg)
	}
}

func (in *Installer) runtimeCheck() error {
	if in.Runtime != nil {
		return in.Runtime()
	}
	return RuntimeCheck()
}

// Dir is the folder of one version. The version is checked, so the result is always directly under Home.
func (in *Installer) Dir(version string) (string, error) {
	if !ValidVersion(version) {
		return "", ErrNotInstalled
	}
	return filepath.Join(in.Home, version), nil
}

// List returns the installed versions, newest first.
func (in *Installer) List() ([]Installed, error) {
	entries, err := os.ReadDir(in.Home)
	if errors.Is(err, os.ErrNotExist) {
		return []Installed{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Installed{}
	for _, e := range entries {
		if !e.IsDir() || !ValidVersion(e.Name()) {
			continue
		}
		dir := filepath.Join(in.Home, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, metaFile))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(data, &m) != nil || m.Version != e.Name() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, ExeName)); err != nil {
			continue
		}
		out = append(out, Installed{Version: m.Version, Dir: dir, Exe: filepath.Join(dir, ExeName), Ini: filepath.Join(dir, "php.ini"), InstalledAt: m.InstalledAt})
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}

// Get returns one installed version.
func (in *Installer) Get(version string) (Installed, error) {
	list, err := in.List()
	if err != nil {
		return Installed{}, err
	}
	for _, v := range list {
		if v.Version == version {
			return v, nil
		}
	}
	return Installed{}, ErrNotInstalled
}

// Remove deletes an installed version. The caller has checked that no pool uses it.
func (in *Installer) Remove(version string) error {
	v, err := in.Get(version)
	if err != nil {
		return err
	}
	return os.RemoveAll(v.Dir)
}

// Install downloads, checks and unpacks one version. version must be one Releases offers. Only one install runs at
// a time. A failure leaves nothing behind.
func (in *Installer) Install(ctx context.Context, version string, progress Progress) (err error) {
	if !in.Supported() {
		return ErrUnsupported
	}
	if !in.busy.TryLock() {
		return ErrBusy
	}
	defer in.busy.Unlock()

	if err := in.runtimeCheck(); err != nil {
		return err
	}
	progress.report(1, "Reading the list of versions")
	rel, err := in.Releases(ctx)
	if err != nil {
		return err
	}
	var want *Release
	for i := range rel {
		if rel[i].Version == version {
			want = &rel[i]
		}
	}
	if want == nil {
		return ErrUnknownVersion
	}
	target := filepath.Join(in.Home, want.Version)
	if _, err := os.Stat(target); err == nil {
		return ErrInstalled
	}
	if err := os.MkdirAll(in.Home, 0o755); err != nil {
		return err
	}

	zipFile, err := os.CreateTemp(in.Home, ".download-*.zip")
	if err != nil {
		return err
	}
	zipPath := zipFile.Name()
	part := target + ".part"
	defer func() {
		zipFile.Close()
		os.Remove(zipPath)
		if err != nil {
			os.RemoveAll(part)
		}
	}()

	// Download, hashing as it goes. The feed's hash decides whether the file is kept.
	progress.report(2, "Downloading PHP "+want.Version)
	resp, err := in.get(ctx, in.base()+want.Path)
	if err != nil {
		return fmt.Errorf("downloading PHP %s: %w", want.Version, err)
	}
	defer resp.Body.Close()
	total := want.Size
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	if total > maxZipSize {
		return errors.New("the download is larger than expected; it was not kept")
	}
	h := sha256.New()
	var got int64
	buf := make([]byte, 64<<10)
	lastPct := -1
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			got += int64(n)
			if got > maxZipSize {
				return errors.New("the download is larger than expected; it was not kept")
			}
			h.Write(buf[:n])
			if _, werr := zipFile.Write(buf[:n]); werr != nil {
				return werr
			}
			if total > 0 {
				if pct := 2 + int(float64(got)/float64(total)*70); pct != lastPct && pct <= 72 {
					lastPct = pct
					progress.report(pct, "Downloading PHP "+want.Version)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("downloading PHP %s: %w", want.Version, rerr)
		}
	}
	if err := zipFile.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return errors.New("the downloaded file does not match the checksum from windows.php.net, so it was thrown away")
	}

	progress.report(75, "Unpacking")
	if err := os.RemoveAll(part); err != nil {
		return err
	}
	if err := unzip(zipPath, part); err != nil {
		return fmt.Errorf("unpacking PHP %s: %w", want.Version, err)
	}
	exe := filepath.Join(part, ExeName)
	if _, err := os.Stat(exe); err != nil {
		return errors.New("the download has no php-cgi.exe; it was not installed")
	}

	progress.report(88, "Checking that PHP starts")
	smoke := in.Smoke
	if smoke == nil {
		smoke = smokeTest
	}
	if err := smoke(exe); err != nil {
		return err
	}

	progress.report(94, "Writing php.ini")
	if err := writeDefaultIni(part, target); err != nil {
		return err
	}
	meta, _ := json.MarshalIndent(Meta{Version: want.Version, Source: in.base() + want.Path, SHA256: want.SHA256, InstalledAt: in.now().UTC()}, "", "  ")
	if err := os.WriteFile(filepath.Join(part, metaFile), meta, 0o644); err != nil {
		return err
	}
	if err := os.Rename(part, target); err != nil {
		return err
	}
	progress.report(100, "PHP "+want.Version+" is installed")
	return nil
}

// smokeTest starts php-cgi with -v. A missing runtime DLL shows up here as a failure to start.
func smokeTest(exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "-v").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the new php-cgi.exe does not start (%v). If Windows named a missing .dll, install the Microsoft Visual C++ Redistributable (x64): %s", err, RuntimeURL)
	}
	if !strings.Contains(string(out), "PHP ") {
		return errors.New("the new php-cgi.exe did not say which PHP it is")
	}
	return nil
}

// unzip extracts a zip into dest (which must not exist yet). Every name is checked: no absolute path, no "..", no
// drive letter, no backslash tricks, no links; the number and total size of the files are capped.
func unzip(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > maxFiles {
		return errors.New("the archive has too many files")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
			return fmt.Errorf("the archive has an unsafe name %q", name)
		}
		clean := path.Clean(name)
		if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
			if clean == "." {
				continue
			}
			return fmt.Errorf("the archive has an unsafe name %q", name)
		}
		if f.Mode()&os.ModeSymlink != 0 || f.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeIrregular) != 0 {
			return fmt.Errorf("the archive has a link or special file %q", name)
		}
		out := filepath.Join(root, filepath.FromSlash(clean))
		if out != root && !strings.HasPrefix(out, root+string(filepath.Separator)) {
			return fmt.Errorf("the archive has an unsafe name %q", name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		total += f.UncompressedSize64
		if total > maxUnpackedSize {
			return errors.New("the archive is larger than expected when unpacked")
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, out); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, out string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	// The header's size can lie: stop at what it promised.
	n, err := io.Copy(dst, io.LimitReader(src, int64(f.UncompressedSize64)+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if uint64(n) > f.UncompressedSize64 {
		return fmt.Errorf("%s is larger than its header says", f.Name)
	}
	return nil
}

// Runtime reports whether the system can run PHP (nil: yes).
func (in *Installer) CheckRuntime() error { return in.runtimeCheck() }
