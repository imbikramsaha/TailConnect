package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Wrong arguments are refused before anything runs: "logout now" must not
// sign out, "cleanup --dry-run" must not remove SSH keys.
func TestCLIRefusesWrongArguments(t *testing.T) {
	// No running app to talk to: a command that got past the argument
	// check would fail differently (exit 1), never 2.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	stderr := os.Stderr
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()
	for _, args := range [][]string{
		{"logout", "now"}, {"login", "x"}, {"quit", "please"}, {"cleanup", "--dry-run"},
		{"status", "x"}, {"devices", "x"}, {"version", "x"}, {"open", "a", "b"},
		{"connect"}, {"connect", "a", "b"}, {"disconnect"}, {"ssh"}, {"proxy", "a"},
		{"screen", "setup", "x"}, {"pair", "approve", "a", "full", "x"}, {"nosuchcommand"},
	} {
		if got := runCLI(args); got != 2 {
			t.Errorf("%q: exit %d, want 2 (usage)", args, got)
		}
	}
}
