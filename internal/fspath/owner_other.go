//go:build !unix

package fspath

import "io/fs"

// owner has no portable meaning here, so the owner_match policy never matches
// and behaves like deny. (Windows deployments isolate sites with ACLs.)
func owner(fi fs.FileInfo) (uint64, bool) { return 0, false }
