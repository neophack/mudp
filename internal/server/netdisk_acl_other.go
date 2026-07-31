//go:build !linux

package server

// fixNetdiskACL is a no-op outside Linux: POSIX ACLs (and the root-owned
// bind-mount problem they work around) are a Linux container concept.
func fixNetdiskACL(dir string) {}
