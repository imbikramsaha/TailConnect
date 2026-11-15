// Package hostname returns a friendly name for this computer.
package hostname

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Friendly returns the user-visible computer name: the macOS Computer Name
// ("Bikram's MacBook Air"), else the hostname without any domain suffix
// (DHCP often appends one, e.g. "mac.router.lan").
func Friendly() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	if h == "" {
		h = "device"
	}
	return h
}
