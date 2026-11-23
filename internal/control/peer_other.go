//go:build !linux

package control

import "net"

// peerInfo identifies the process on the other end of a connection. Only
// Linux reports it for now (FreeBSD's getpeereid and the Windows token are
// not wired up); the audit log then records -1.
type peerInfo struct {
	UID, PID int
	Known    bool
}

func peerOf(net.Conn) peerInfo { return peerInfo{UID: -1, PID: -1} }
