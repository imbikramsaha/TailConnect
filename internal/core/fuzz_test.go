package core

import (
	"strings"
	"testing"
	"unicode"
)

// Accepted peer values never contain characters ssh_config or cmd.exe act on.
func FuzzSanitizers(f *testing.F) {
	f.Add("bob", "/home/bob")
	f.Add("x\n%d", `C:\x&calc`)
	f.Fuzz(func(t *testing.T, user, home string) {
		if u, ok := SafeUser(user); ok && (strings.ContainsAny(u, "\"'`\\%#$;|&<>*?=\n\r") || strings.ContainsFunc(u, unicode.IsControl)) {
			t.Fatalf("SafeUser accepted %q", u)
		}
		if h, ok := SafeHome(home); ok && (strings.ContainsAny(h, "&|<>^()!%\"`'$;\n\r") || strings.ContainsFunc(h, unicode.IsControl)) {
			t.Fatalf("SafeHome accepted %q", h)
		}
		if n := SafeName(user); strings.ContainsFunc(n, unicode.IsControl) || len([]rune(n)) > 64 {
			t.Fatalf("SafeName(%q) = %q", user, n)
		}
	})
}
