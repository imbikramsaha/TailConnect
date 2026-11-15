package window

import (
	"os"
	"path/filepath"
)

// On Linux the window lives in a separate helper (tailconnect-window) so the
// main binary runs on servers without GTK/WebKit installed.
func command(exe string) ([]string, bool) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, false
	}
	for _, p := range []string{
		filepath.Join(filepath.Dir(exe), "tailconnect-window"),
		"/usr/lib/tailconnect/tailconnect-window",
		"/usr/libexec/tailconnect/tailconnect-window",
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return []string{p}, true
		}
	}
	return nil, false
}
