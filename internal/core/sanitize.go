package core

import (
	"net"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
)

// Values reported by other devices (Hello / PairResponse) end up in
// ~/.ssh/config, command arguments, notifications and the UI. Treat them as
// hostile: accept only plain values, never control characters or anything
// ssh_config would interpret (quotes, %-tokens, comments).

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// SafeUser validates a remote login name.
func SafeUser(s string) (string, bool) {
	if s == "" || len(s) > 64 || hasControl(s) || strings.ContainsAny(s, "\"'`\\%#$;|&<>*?=") ||
		strings.HasPrefix(s, "-") || strings.TrimSpace(s) != s {
		return "", false
	}
	return s, true
}

// A home directory ends up in editor command lines; on Windows those go
// through .cmd shims, where cmd.exe interprets & | < > ^ ( ) ! %. Allow only
// letters (any script), digits and a few safe punctuation characters.
var (
	posixHome = regexp.MustCompile(`^/[\p{L}\p{N} ._@+~/-]{0,510}$`)
	winHome   = regexp.MustCompile(`^[A-Za-z]:\\[\p{L}\p{N} ._@+~\\-]{0,510}$`)
)

// SafeHome validates a remote home directory (absolute POSIX or Windows path).
func SafeHome(s string) (string, bool) {
	if posixHome.MatchString(s) || winHome.MatchString(s) {
		return s, true
	}
	return "", false
}

var osNameRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// SafeOS validates a GOOS-style OS name.
func SafeOS(s string) string {
	if osNameRe.MatchString(s) {
		return s
	}
	return ""
}

// SafeName cleans a display name: no control characters, bounded length.
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '‮' || r == '‭' { // incl. bidi overrides
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

// safeMACs keeps up to 8 valid 48-bit MAC addresses.
func safeMACs(in []string) []string {
	var out []string
	for _, m := range in {
		if hw, err := net.ParseMAC(m); err == nil && len(hw) == 6 && len(out) < 8 {
			out = append(out, hw.String())
		}
	}
	return out
}

// safeLANs keeps up to 8 private IPv4 networks.
func safeLANs(in []string) []string {
	var out []string
	for _, l := range in {
		p, err := netip.ParsePrefix(l)
		if err == nil && p.Addr().Is4() && p.Addr().IsPrivate() && p.Bits() >= 8 && len(out) < 8 {
			out = append(out, p.Masked().String())
		}
	}
	return out
}
