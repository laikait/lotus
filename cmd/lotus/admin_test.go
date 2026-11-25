package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/admin"
)

func TestAdminUsersPath(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "main.conf")
	if got := adminUsersPath(cfg, nil); got != filepath.Join(filepath.Dir(cfg), "admin-users.txt") {
		t.Errorf("default: %s", got)
	}
}

func TestDecodeText(t *testing.T) {
	utf16le := []byte{'p', 0, 'w', 0, '\r', 0, '\n', 0}
	for name, in := range map[string][]byte{
		"plain":        []byte("pw\r\n"),
		"utf8 bom":     append([]byte{0xEF, 0xBB, 0xBF}, "pw\n"...),
		"utf16 bom":    append([]byte{0xFF, 0xFE}, utf16le...),
		"utf16 no bom": utf16le,
	} {
		if got := firstLine(decodeText(in)); got != "pw" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestAdminPasswdFromStdinAndUsers(t *testing.T) {
	dir := t.TempDir()
	users := filepath.Join(dir, "u.txt")

	r, w, _ := os.Pipe()
	_, _ = w.WriteString("a long enough password\n")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	var out, errb bytes.Buffer
	if code := run([]string{"admin", "passwd", "ada", "--password-stdin", "--users", users}, &out, &errb); code != exitOK {
		t.Fatalf("passwd: %d %s", code, errb.String())
	}
	if _, ok := (admin.Users{Path: users}).Verify("ada", "a long enough password"); !ok {
		t.Error("the account does not accept its password")
	}

	out.Reset()
	if code := run([]string{"admin", "users", "--users", users}, &out, &errb); code != exitOK || !strings.Contains(out.String(), "ada\tadmin") {
		t.Errorf("users: %d %q", code, out.String())
	}
	if code := run([]string{"admin", "passwd", "../x", "--users", users}, &out, &errb); code == exitOK {
		t.Error("a bad name was accepted")
	}
}
