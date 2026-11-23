package control

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// DefaultSocketPath is where the control socket lives when the configuration
// doesn't say (control-protocol.md §1). Unprivileged users get a per-user
// location so a development server can run without root.
func DefaultSocketPath() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "LOTUS", "control.sock")
	case "darwin":
		return filepath.Join(os.TempDir(), "lotus", "control.sock")
	}
	if os.Geteuid() == 0 {
		return "/run/lotus/control.sock"
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "lotus", "control.sock")
	}
	return filepath.Join(os.TempDir(), "lotus-"+strconv.Itoa(os.Geteuid()), "control.sock")
}

// maxSocketPath is the longest Unix socket path the system accepts (the size
// of sockaddr_un.sun_path minus the terminating NUL). Linux and Windows allow
// 108 bytes; macOS and the BSDs only 104. Longer paths make bind fail with the
// unhelpful "invalid argument".
func maxSocketPath() int {
	switch runtime.GOOS {
	case "linux", "android", "windows":
		return 107
	}
	return 103
}

// Listen creates the control socket at path.
//
// The directory is created if it is missing (owner-only) and must not be
// writable by anyone else, or another user could replace the socket. A socket
// file left by a crashed server is removed, but only after a test connection
// shows nothing is listening; a live one is an error. The socket is made
// accessible to its owner only.
func Listen(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("the control socket path %q must be absolute", path)
	}
	if limit := maxSocketPath(); len(path) > limit {
		return nil, fmt.Errorf("the control socket path is %d bytes long, but this system allows at most %d: use a shorter path (control { socket ... } or --control)", len(path), limit)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("control socket directory: %w", err)
	}
	if err := checkDirectory(dir); err != nil {
		return nil, err
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if err := restrictSocket(path); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, err
	}
	// Closing the listener removes the socket file (net does this for unix sockets
	// it created), so a clean shutdown leaves nothing behind.
	return ln, nil
}

// removeStale deletes a leftover socket file if no server answers on it.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("control socket path %s exists and is not a socket; not touching it", path)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		conn.Close()
		return fmt.Errorf("another lotus is already listening on %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing the stale control socket: %w", err)
	}
	return nil
}
