package htaccess

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HTTP Basic authentication against an htpasswd file.
//
// Supported: AuthType Basic, AuthName, AuthUserFile, AuthBasicProvider file,
// with `Require valid-user` / `Require user ...` (access.go). Password hashes:
// bcrypt ($2y$, $2a$, $2b$ — htpasswd -B), Apache MD5 ($apr1$ — the htpasswd
// default) and {SHA}. Plain text and crypt(3) entries are not accepted: a user
// whose entry LOTUS can't verify can never log in. Digest authentication,
// AuthGroupFile and Require group are refused.

// authSpec is the Auth* directives of one section.
type authSpec struct {
	typeSet, basic bool
	realmSet       bool
	realm          string
	fileSet        bool
	file           string
}

func (c *compiler) auth(n *Node, sec *section) {
	if sec.auth == nil {
		sec.auth = &authSpec{}
	}
	a := sec.auth
	switch n.Name {
	case "authtype":
		if len(n.Args) != 1 || !strings.EqualFold(n.Args[0], "basic") {
			c.bad(n.Line, n.Name, "only `AuthType Basic` is supported (Digest and form authentication are not)", true)
			return
		}
		a.typeSet, a.basic = true, true
	case "authname":
		if len(n.Args) != 1 {
			c.bad(n.Line, n.Name, `use: AuthName "Realm"`, true)
			return
		}
		for i := 0; i < len(n.Args[0]); i++ {
			if n.Args[0][i] < 0x20 || n.Args[0][i] == 0x7f {
				c.bad(n.Line, n.Name, "the realm can't contain control characters", true)
				return
			}
		}
		a.realmSet, a.realm = true, n.Args[0]
	case "authuserfile":
		if len(n.Args) != 1 || n.Args[0] == "" || strings.ContainsRune(n.Args[0], 0) {
			c.bad(n.Line, n.Name, "use: AuthUserFile /path/to/.htpasswd", true)
			return
		}
		a.fileSet, a.file = true, n.Args[0]
	case "authbasicprovider":
		if len(n.Args) != 1 || !strings.EqualFold(n.Args[0], "file") {
			c.bad(n.Line, n.Name, "only `AuthBasicProvider file` is supported", true)
			return
		}
	}
	c.ok(n)
}

// mergeAuth gives the authentication settings in force for a request: the
// deeper a file, the more it overrides. fileDir is the directory of the file
// that set AuthUserFile, which a relative path is read from.
func mergeAuth(layers []Layer, base string) (a authSpec, fileDir string) {
	for _, l := range layers {
		for _, s := range l.File.sections {
			if s.auth == nil || !(s.match == nil || (base != "" && s.match(base))) {
				continue
			}
			if s.auth.typeSet {
				a.typeSet, a.basic = true, s.auth.basic
			}
			if s.auth.realmSet {
				a.realmSet, a.realm = true, s.auth.realm
			}
			if s.auth.fileSet {
				a.fileSet, a.file, fileDir = true, s.auth.file, l.DirURL
			}
		}
	}
	return a, fileDir
}

// MaxBcryptCost is the highest bcrypt cost that is checked (about a quarter of
// a second per attempt); entries with a higher cost never match.
const MaxBcryptCost = 12

// MaxHtpasswdSize bounds the password files that are read.
const MaxHtpasswdSize = 4 << 20

// ParseHtpasswd reads "user:hash" lines. Blank lines and # comments are skipped.
func ParseHtpasswd(data []byte) map[string]string {
	users := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		user, hash, ok := strings.Cut(line, ":")
		if !ok || user == "" || hash == "" {
			continue
		}
		if _, dup := users[user]; !dup { // Apache uses the first entry
			users[user] = hash
		}
	}
	return users
}

// VerifyPassword says whether password matches an htpasswd hash. Unknown hash
// formats never match.
func VerifyPassword(hash, password string) bool {
	switch {
	case strings.HasPrefix(hash, "$2y$"), strings.HasPrefix(hash, "$2a$"), strings.HasPrefix(hash, "$2b$"), strings.HasPrefix(hash, "$2x$"):
		if strings.HasPrefix(hash, "$2x$") {
			return false // the buggy variant; Go's bcrypt is not compatible with it
		}
		// The password file is written by the site, and a hash may declare any
		// cost up to 31: one login attempt against such an entry would keep a CPU
		// busy for hours. Costs people really use (5 to 12) are fine.
		if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost > MaxBcryptCost {
			return false
		}
		if len(password) > 72 { // bcrypt reads 72 bytes; Go refuses longer input
			password = password[:72]
		}
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	case strings.HasPrefix(hash, "$apr1$"):
		return subtle.ConstantTimeCompare([]byte(apr1(password, hash)), []byte(hash)) == 1
	case strings.HasPrefix(hash, "{SHA}"):
		sum := sha1.Sum([]byte(password))
		want := "{SHA}" + base64.StdEncoding.EncodeToString(sum[:])
		return subtle.ConstantTimeCompare([]byte(want), []byte(hash)) == 1
	}
	return false
}

// ParseBasic reads an Authorization header of the Basic scheme.
func ParseBasic(header string) (user, pass string, ok bool) {
	const prefix = "basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(raw), ":")
	if !ok || user == "" {
		return "", "", false
	}
	return user, pass, true
}

// apr1 is Apache's MD5-based hash ($apr1$salt$digest), the default of htpasswd.
// salt is taken from an existing hash.
func apr1(password, hash string) string {
	salt := strings.TrimPrefix(hash, "$apr1$")
	if i := strings.IndexByte(salt, '$'); i >= 0 {
		salt = salt[:i]
	}
	if len(salt) > 8 {
		salt = salt[:8]
	}
	pw := []byte(password)
	alt := md5.Sum([]byte(password + salt + password))
	h := md5.New()
	h.Write(pw)
	h.Write([]byte("$apr1$" + salt))
	for n := len(pw); n > 0; n -= 16 {
		if n > 16 {
			h.Write(alt[:])
		} else {
			h.Write(alt[:n])
		}
	}
	for n := len(pw); n > 0; n >>= 1 {
		if n&1 == 1 {
			h.Write([]byte{0})
		} else {
			h.Write(pw[:1])
		}
	}
	final := h.Sum(nil)
	for i := 0; i < 1000; i++ {
		h := md5.New()
		if i&1 == 1 {
			h.Write(pw)
		} else {
			h.Write(final)
		}
		if i%3 != 0 {
			h.Write([]byte(salt))
		}
		if i%7 != 0 {
			h.Write(pw)
		}
		if i&1 == 1 {
			h.Write(final)
		} else {
			h.Write(pw)
		}
		final = h.Sum(nil)
	}
	const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	enc := func(a, b, c byte, n int) string {
		v := uint(a)<<16 | uint(b)<<8 | uint(c)
		out := make([]byte, n)
		for i := 0; i < n; i++ {
			out[i] = itoa64[v&0x3f]
			v >>= 6
		}
		return string(out)
	}
	var sb strings.Builder
	sb.WriteString("$apr1$" + salt + "$")
	sb.WriteString(enc(final[0], final[6], final[12], 4))
	sb.WriteString(enc(final[1], final[7], final[13], 4))
	sb.WriteString(enc(final[2], final[8], final[14], 4))
	sb.WriteString(enc(final[3], final[9], final[15], 4))
	sb.WriteString(enc(final[4], final[10], final[5], 4))
	sb.WriteString(enc(0, 0, final[11], 2))
	return sb.String()
}

// HashAPR1 is exported for tests and tools that write htpasswd entries.
func HashAPR1(password, salt string) string {
	return apr1(password, fmt.Sprintf("$apr1$%s$", salt))
}
