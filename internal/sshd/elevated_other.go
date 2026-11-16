//go:build !(darwin || linux || freebsd || windows)

package sshd

// Elevated reports whether this process runs with administrator rights.
func Elevated() bool { return false }
