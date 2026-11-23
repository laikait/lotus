//go:build linux

package control

import (
	"net"
	"syscall"
)

// peerInfo identifies the process on the other end of a connection.
type peerInfo struct {
	UID, PID int
	Known    bool
}

// peerOf reads the peer's credentials with SO_PEERCRED. They are used for the
// audit log; access control is the socket's file permissions.
func peerOf(c net.Conn) peerInfo {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return peerInfo{UID: -1, PID: -1}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return peerInfo{UID: -1, PID: -1}
	}
	info := peerInfo{UID: -1, PID: -1}
	_ = raw.Control(func(fd uintptr) {
		if cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED); err == nil {
			info = peerInfo{UID: int(cred.Uid), PID: int(cred.Pid), Known: true}
		}
	})
	return info
}
