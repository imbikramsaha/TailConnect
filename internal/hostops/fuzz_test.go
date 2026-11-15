package hostops

import (
	"path/filepath"
	"strings"
	"testing"
)

// Received file names can never leave the inbox or hide as a device name.
func FuzzSafeName(f *testing.F) {
	for _, s := range []string{"../../etc/passwd", `..\..\x`, "CON.txt", "a\x00b", "\u202etxt.exe", "/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		n, err := SafeName(name)
		if err != nil {
			return
		}
		if strings.ContainsAny(n, `/\<>:"|?*`) || n == "." || n == ".." || strings.HasPrefix(n, ".") ||
			filepath.Base(n) != n || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || (r >= 0x202a && r <= 0x202e) }) {
			t.Fatalf("SafeName(%q) = %q", name, n)
		}
	})
}

func FuzzParsers(f *testing.F) {
	f.Add("p1\ncx\nn127.0.0.1:80\nss LISTEN 0 1 [::]:22 x users:((\"a\",pid=1,fd=2))\ngpu 1, 2\n")
	f.Fuzz(func(t *testing.T, s string) {
		_ = ParseLsof(s)
		_ = ParseRemoteServices(s)
		_ = ParseRemoteProcs(s)
		_ = ParseContainers(s)
		_ = ParseGPUProcs(s)
	})
}
