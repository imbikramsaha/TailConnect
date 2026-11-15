package autostart

import "testing"

func TestDesktopQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/tailconnect":   `"/usr/bin/tailconnect"`,
		`/opt/a "b"/tc`:          `"/opt/a \\"b\\"/tc"`,
		"/home/x/100%/$HOME/`t`": "\"/home/x/100%%/\\\\$HOME/\\\\`t\\\\`\"",
	} {
		if got := desktopQuote(in); got != want {
			t.Errorf("desktopQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
