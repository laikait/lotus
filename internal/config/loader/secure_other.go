//go:build !unix

package loader

// checkSecure has no equivalent of Unix mode bits and ownership checks on this
// platform. Windows deployments protect the configuration directory with ACLs;
// see the platform notes in the operations documentation.
func checkSecure(name string) error { return nil }
