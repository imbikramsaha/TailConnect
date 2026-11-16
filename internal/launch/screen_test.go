package launch

import (
	"runtime"
	"strings"
	"testing"
)

func TestScreenViewers(t *testing.T) {
	// Built-in viewers are always found; the address is always the local tunnel.
	switch runtime.GOOS {
	case "darwin":
		if argv := screenViewer(ScreenVNC, 5901); strings.Join(argv, " ") != "open vnc://127.0.0.1:5901" {
			t.Errorf("macOS VNC: %q", argv)
		}
	case "windows":
		if argv := screenViewer(ScreenRDP, 3390); strings.Join(argv, " ") != "mstsc /v:127.0.0.1:3390" {
			t.Errorf("Windows RDP: %q", argv)
		}
	}
	// A missing viewer explains what to install instead of failing silently.
	for _, kind := range []string{ScreenRustDesk, ScreenRDP, ScreenVNC} {
		ok, hint := ScreenViewer(kind)
		if !ok && hint == "" {
			t.Errorf("%s: no viewer and no hint", kind)
		}
		if ok {
			argv := screenViewer(kind, 4000)
			if len(argv) == 0 || !strings.Contains(strings.Join(argv, " "), "127.0.0.1") {
				t.Errorf("%s: viewer %q doesn't target the local tunnel", kind, argv)
			}
		}
	}
	if err := OpenScreen("bogus", 1); err == nil {
		t.Error("unknown kind opened something")
	}
}
