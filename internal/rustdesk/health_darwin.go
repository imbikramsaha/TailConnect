package rustdesk

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// NeedsRepair reports whether this user's RustDesk settings include files
// owned by root (left by TailConnect 0.3.9's setup), which the user's
// RustDesk can't read: it then runs without direct access or its password.
// Setting up again repairs them.
func NeedsRepair() bool {
	home, err := os.UserHomeDir()
	if err != nil || os.Geteuid() == 0 {
		return false
	}
	dir := filepath.Join(home, "Library", "Preferences", "com.carriez.RustDesk")
	if rootOwned(dir) {
		return true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "RustDesk") && strings.HasSuffix(n, ".toml") && rootOwned(filepath.Join(dir, n)) {
			return true
		}
	}
	return false
}

func rootOwned(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
