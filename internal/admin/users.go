// Package admin is the web admin: a sign-in page and a dashboard served on a
// listener of its own (docs/architecture/admin.md). It talks to the running
// server only through control.Backend, the same operations the lotus command
// uses.
package admin

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Roles. An admin may change things; a viewer may only look.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// argon2id parameters (RFC 9106's second recommended option, lighter on memory).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

var userNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether name can be an account name.
func ValidName(name string) bool { return userNameRe.MatchString(name) }

// User is one account of the users file.
type User struct {
	Name string
	Role string
	Hash string // PHC string: $argon2id$v=19$m=...,t=...,p=...$salt$key
}

// HashPassword makes the stored form of a password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// checkPassword compares a password with a stored hash in constant time.
func checkPassword(password, hash string) bool {
	var version, m, t, p int
	parts := strings.Split(hash, "$") // "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m < 8 || m > 1<<21 || t < 1 || t > 20 || p < 1 || p > 64 { // refuse a hash that would make a sign-in cost too much
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Users reads and writes the users file. Every call reads the file again, so a
// password changed with `lotus admin passwd` works without a restart.
type Users struct{ Path string }

// List returns the accounts, sorted by name. A missing file is an empty list.
func (u Users) List() ([]User, error) {
	f, err := os.Open(u.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []User
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.SplitN(text, ":", 3)
		if len(parts) != 3 || !ValidName(parts[0]) || (parts[1] != RoleAdmin && parts[1] != RoleViewer) {
			return nil, fmt.Errorf("%s line %d: expected name:role:hash", u.Path, line)
		}
		out = append(out, User{Name: parts[0], Role: parts[1], Hash: parts[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// dummyHash is checked when the name is unknown, so that a wrong name costs as much time as a wrong password.
var dummyHash, _ = HashPassword("not a real password")

// Verify checks a name and password and returns the account.
func (u Users) Verify(name, password string) (User, bool) {
	list, err := u.List()
	if err != nil {
		return User{}, false
	}
	for _, c := range list {
		if c.Name == name {
			return c, checkPassword(password, c.Hash)
		}
	}
	checkPassword(password, dummyHash)
	return User{}, false
}

// Set adds or replaces an account. The file is written whole to a temporary
// file and renamed over the old one, readable by its owner only.
func (u Users) Set(name, role, password string) error {
	if !ValidName(name) {
		return errors.New("the name may use letters, digits, dot, dash and underscore, and must start with a letter or digit")
	}
	if role != RoleAdmin && role != RoleViewer {
		return fmt.Errorf("the role must be %q or %q", RoleAdmin, RoleViewer)
	}
	if len(password) < 10 {
		return errors.New("the password must be at least 10 characters")
	}
	if len(password) > 256 {
		return errors.New("the password is too long (at most 256 characters)")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	list, err := u.List()
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].Name == name {
			list[i] = User{Name: name, Role: role, Hash: hash}
			replaced = true
		}
	}
	if !replaced {
		list = append(list, User{Name: name, Role: role, Hash: hash})
	}
	return u.write(list)
}

// write replaces the users file with list.
func (u Users) write(list []User) error {
	var b strings.Builder
	b.WriteString("# LOTUS admin accounts: name:role:password hash. Change them with \"lotus admin passwd\", not by hand.\n")
	for _, c := range list {
		fmt.Fprintf(&b, "%s:%s:%s\n", c.Name, c.Role, c.Hash)
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(u.Path), ".admin-users-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), u.Path)
}

// ErrNoUser means there is no account of that name.
var ErrNoUser = errors.New("there is no such account")

// Delete removes an account.
func (u Users) Delete(name string) error {
	list, err := u.List()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].Name == name {
			return u.write(append(list[:i], list[i+1:]...))
		}
	}
	return ErrNoUser
}

// SetRole changes the role of an account and keeps its password.
func (u Users) SetRole(name, role string) error {
	if role != RoleAdmin && role != RoleViewer {
		return fmt.Errorf("the role must be %q or %q", RoleAdmin, RoleViewer)
	}
	list, err := u.List()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].Name == name {
			list[i].Role = role
			return u.write(list)
		}
	}
	return ErrNoUser
}

// Admins counts the accounts with the admin role.
func Admins(list []User) int {
	n := 0
	for _, c := range list {
		if c.Role == RoleAdmin {
			n++
		}
	}
	return n
}
