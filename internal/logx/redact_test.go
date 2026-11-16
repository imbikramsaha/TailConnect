package logx

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, mustNot, must string }{
		{"To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/gc9e283310d265a75", "gc9e283310d265a75", "https://login.tailscale.com/a/[redacted]"},
		{"go to https://hs.example.com/register/mkey:abc123DEF and log in", "mkey:abc123DEF", "/register/[redacted] and log in"},
		{"using tskey-auth-kAbC123-XyZ987 for login", "kAbC123", "tskey-[redacted]"},
		{"opened http://127.0.0.1:47474/auth?t=0987ab05656b2b3ae627ed28ee068448", "0987ab05656b", "auth?t=[redacted]"},
		{"window at http://127.0.0.1:1/#s=" + strings.Repeat("ab", 32) + "&r=/", strings.Repeat("ab", 32), "#s=[redacted]&r=/"},
		{"jupyter at http://localhost:8888/lab?token=s3cr3tvalue", "s3cr3tvalue", "token=[redacted]"},
		{`config {"password": "hunter2"}`, "hunter2", `"password": "[redacted]`},
		{"Authorization: Bearer abc.def.ghi", "abc.def.ghi", "Bearer [redacted]"},
		{"key -----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY----- done", "AAAA", "[redacted private key] done"},
	} {
		got := Redact(tc.in)
		if strings.Contains(got, tc.mustNot) || !strings.Contains(got, tc.must) {
			t.Errorf("Redact(%q) = %q", tc.in, got)
		}
	}
	// Ordinary log lines are untouched.
	for _, s := range []string{
		"TailConnect 0.3.6 ready (control API on 127.0.0.1:47474)",
		"session: connected to Mac mini (tc-mac-mini) via direct 192.168.1.4:41641",
		"agent: pair request from alpha (user@example.com): approved",
		"tray connect: dial tcp 100.64.0.2:4747: i/o timeout",
	} {
		if got := Redact(s); got != s {
			t.Errorf("changed an ordinary line: %q -> %q", s, got)
		}
	}
}
