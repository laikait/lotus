//go:build windows

package control

// On Windows a socket is a file whose access is governed by ACLs, inherited
// from its directory. The directory under %ProgramFiles% is created with the
// default ACL (administrators and SYSTEM full control, users read-only), so
// other users can't replace the socket. Checking ACLs here would need the
// Windows security API; until then the default location is the control.
func checkDirectory(string) error { return nil }

func restrictSocket(string) error { return nil }
