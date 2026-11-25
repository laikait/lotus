package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/laikait/lotus/internal/admin"
	"github.com/laikait/lotus/internal/config"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/sitefile"
)

// adminUsersPath is the users file of an admin block: its users_file, or admin-users.txt next to the main
// configuration file. A relative users_file is read from the same folder.
func adminUsersPath(cfgPath string, a *model.Admin) string {
	dir := filepath.Dir(cfgPath)
	if a == nil || a.UsersFile == "" {
		return filepath.Join(dir, "admin-users.txt")
	}
	if filepath.IsAbs(a.UsersFile) {
		return a.UsersFile
	}
	return filepath.Join(dir, a.UsersFile)
}

// openAdmin starts the web admin when the configuration has an admin block. It returns what to close on shutdown.
// The listener is opened here, so an address in use fails the start instead of leaving the admin silently off.
func (a *app) openAdmin(cfg *model.Config) (io.Closer, error) {
	ad := cfg.Admin
	if ad == nil {
		return nil, nil
	}
	ln, err := net.Listen("tcp", ad.Address)
	if err != nil {
		return nil, err
	}
	secure := false
	if ad.Certificate != "" {
		cert, err := tls.LoadX509KeyPair(ad.Certificate, ad.CertificateKey)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("admin certificate: %w", err)
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}})
		secure = true
	}
	users := admin.Users{Path: adminUsersPath(a.path, ad)}
	if list, err := users.List(); err != nil {
		ln.Close()
		return nil, err
	} else if len(list) == 0 {
		a.log.Warn("the admin has no accounts: nobody can sign in", "users_file", users.Path, "fix", "run: lotus admin passwd NAME")
	}
	srv := admin.New(admin.Options{
		Backend: a, Sites: &sitefile.Store{Config: a.path}, PHP: a.phpPools, Users: users, SessionTTL: ad.SessionTTL, Allow: ad.Allow, Secure: secure, Log: a.log,
	})
	go func() {
		if err := srv.Serve(ln); err != nil {
			a.log.Error("the admin listener stopped", "error", err)
		}
	}()
	scheme := "http"
	if secure {
		scheme = "https"
	}
	a.log.Info("admin ready", "url", scheme+"://"+ad.Address+"/")
	return adminCloser{srv}, nil
}

// phpPools describes the managed php-cgi pools for the admin's PHP page.
func (a *app) phpPools() []admin.PHPPool {
	infos := a.srv.PHPPools()
	out := make([]admin.PHPPool, 0, len(infos))
	for _, i := range infos {
		out = append(out, admin.PHPPool{Name: i.Name, Command: i.Command, Processes: i.Processes, Alive: i.Alive, Restarts: i.Restarts})
	}
	return out
}

type adminCloser struct{ s *admin.Server }

func (c adminCloser) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.s.Shutdown(ctx)
}

func cmdAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: lotus admin <passwd|users> [options]")
		return exitUsage
	}
	switch args[0] {
	case "passwd":
		return cmdAdminPasswd(args[1:], stdout, stderr)
	case "users":
		return cmdAdminUsers(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lotus admin: unknown subcommand %q\n\nsubcommands: passwd, users\n", args[0])
		return exitUsage
	}
}

// usersFileFlags adds --config and --users and returns a function that gives the users file.
func usersFileFlags(fs *flag.FlagSet) func() string {
	cfg := fs.String("config", defaultConfigPath(), "main configuration file (its admin block names the users file)")
	users := fs.String("users", "", "users file (default: from the admin block, or admin-users.txt next to the configuration)")
	return func() string {
		if *users != "" {
			return *users
		}
		return adminUsersPath(*cfg, adminBlockOf(*cfg))
	}
}

// adminBlockOf reads the admin block of a configuration file; any problem (no file, an invalid one) means "use the
// default users file".
func adminBlockOf(cfgPath string) *model.Admin {
	res, _ := config.Check(cfgPath, loader.Options{})
	if res == nil || res.Config == nil {
		return nil
	}
	return res.Config.Admin
}

func cmdAdminPasswd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus admin passwd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	usersPath := usersFileFlags(fs)
	role := fs.String("role", admin.RoleAdmin, "admin (may change things) or viewer (read only)")
	stdin := fs.Bool("password-stdin", false, "read the password from standard input (one line), for scripts")
	pwFile := fs.String("password-file", "", "read the password from the first line of this file (the installer uses it, then deletes the file)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lotus admin passwd [--role admin|viewer] [--password-stdin] [--config FILE] [--users FILE] <name>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	name := fs.Arg(0)
	if !admin.ValidName(name) {
		fmt.Fprintln(stderr, "lotus: the name may use letters, digits, dot, dash and underscore, and must start with a letter or digit")
		return exitUsage
	}

	var password string
	if *stdin || *pwFile != "" {
		src := io.Reader(os.Stdin)
		if *pwFile != "" {
			f, err := os.Open(*pwFile)
			if err != nil {
				fmt.Fprintf(stderr, "lotus: %v\n", err)
				return exitUsage
			}
			defer f.Close()
			src = f
		}
		raw, _ := io.ReadAll(io.LimitReader(src, 4096))
		password = firstLine(decodeText(raw))
		if password == "" {
			fmt.Fprintln(stderr, "lotus: no password on standard input")
			return exitUsage
		}
	} else {
		first, err := promptPassword(stderr, "Password for "+name+": ")
		if err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitUsage
		}
		second, err := promptPassword(stderr, "Again: ")
		if err != nil {
			fmt.Fprintf(stderr, "lotus: %v\n", err)
			return exitUsage
		}
		if first != second {
			fmt.Fprintln(stderr, "lotus: the two passwords differ; nothing changed")
			return exitUsage
		}
		password = first
	}

	file := usersPath()
	if err := (admin.Users{Path: file}).Set(name, *role, password); err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "account %s (%s) saved in %s\nIt works at once; no reload is needed.\n", name, *role, file)
	return exitOK
}

func cmdAdminUsers(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lotus admin users", flag.ContinueOnError)
	fs.SetOutput(stderr)
	usersPath := usersFileFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	list, err := (admin.Users{Path: usersPath()}).List()
	if err != nil {
		fmt.Fprintf(stderr, "lotus: %v\n", err)
		return exitUsage
	}
	if len(list) == 0 {
		fmt.Fprintf(stdout, "no accounts in %s (make one with: lotus admin passwd NAME)\n", usersPath())
		return exitOK
	}
	for _, u := range list {
		fmt.Fprintf(stdout, "%s\t%s\n", u.Name, u.Role)
	}
	return exitOK
}

// decodeText turns what a shell piped in into text. Windows PowerShell 5.1 may pipe UTF-16 or put a byte order mark
// in front, which would make the password differ from the one typed in the sign-in form.
func decodeText(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		b = b[2:]
		fallthrough
	case len(b) >= 2 && b[1] == 0 && b[0] != 0: // UTF-16LE without a mark
		units := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(units))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return string(b[3:])
	}
	return string(b)
}

// firstLine is the text up to the first line break.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// promptPassword asks for a password without showing it, when standard input is a terminal.
func promptPassword(out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	restore := echoOff()
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	restore()
	fmt.Fprintln(out)
	if err != nil && line == "" {
		return "", fmt.Errorf("no password given")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
