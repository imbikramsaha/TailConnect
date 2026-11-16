package sshd

import "golang.org/x/sys/windows"

// Elevated reports whether this process runs with an elevated
// (administrator) token, e.g. after being started from an admin prompt.
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
