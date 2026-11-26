package sitefile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// HistoryKeep is how many earlier versions are kept for each file.
const HistoryKeep = 20

// Version is an earlier text of a configuration file: what the file held before a change.
type Version struct {
	ID   string    `json:"id"` // 20261126-153045-ada
	Time time.Time `json:"time"`
	Who  string    `json:"who"`
	Size int64     `json:"size"`
}

// ErrBadVersion means the id is not one this package made.
var ErrBadVersion = errors.New("there is no such version")

var versionRe = regexp.MustCompile(`^([0-9]{8}-[0-9]{6})-([A-Za-z0-9._-]{1,64})$`)

// HistoryDir is where the versions are kept: a history folder next to the main file, outside sites.d, so that no
// include reads a version as configuration.
func (s *Store) HistoryDir() string { return filepath.Join(filepath.Dir(s.Config), "history") }

// folder maps an editor name to its history folder. The editor name is checked first, so no other path gets in.
func (s *Store) folder(name string) (string, error) {
	if _, err := s.path(name); err != nil {
		return "", err
	}
	return filepath.Join(s.HistoryDir(), strings.ReplaceAll(name, "/", "__")), nil
}

// Snapshot keeps old (the text a file had before a change made by who) as a version. Nothing is kept for an empty
// text or for a text equal to the newest version. The oldest versions beyond HistoryKeep are removed.
func (s *Store) Snapshot(name string, old []byte, who string) error {
	dir, err := s.folder(name)
	if err != nil {
		return err
	}
	if len(old) == 0 {
		return nil
	}
	who = cleanWho(who)
	if list, _ := s.History(name); len(list) > 0 {
		if prev, err := os.ReadFile(filepath.Join(dir, list[0].ID+".conf")); err == nil && bytes.Equal(prev, old) {
			return nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	at := time.Now()
	var file string
	for { // two changes in one second get the next second, never overwrite
		file = filepath.Join(dir, at.Format("20060102-150405")+"-"+who+".conf")
		if _, err := os.Stat(file); err != nil {
			break
		}
		at = at.Add(time.Second)
	}
	if err := atomicWrite(file, old); err != nil {
		return err
	}
	if list, err := s.History(name); err == nil {
		for _, v := range list[min(len(list), HistoryKeep):] {
			_ = os.Remove(filepath.Join(dir, v.ID+".conf"))
		}
	}
	return nil
}

func cleanWho(who string) string {
	var b strings.Builder
	for _, r := range who {
		if r < 128 && (r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// History lists the versions of a file, newest first. A file with none gives an empty list.
func (s *Store) History(name string) ([]Version, error) {
	dir, err := s.folder(name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Version{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Version{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".conf")
		m := versionRe.FindStringSubmatch(id)
		if !ok || m == nil || e.IsDir() {
			continue
		}
		at, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Version{ID: id, Time: at, Who: m[2], Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ReadVersion returns the text of one version.
func (s *Store) ReadVersion(name, id string) (string, error) {
	dir, err := s.folder(name)
	if err != nil {
		return "", err
	}
	if !versionRe.MatchString(id) {
		return "", ErrBadVersion
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".conf"))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrBadVersion
	}
	return string(data), err
}

// SiteFileName is the editor name of the file of the site called name.
func SiteFileName(name string) string { return "sites.d/" + name + ".conf" }
