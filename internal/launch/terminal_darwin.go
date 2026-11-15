package launch

import (
	"os"
	"strings"
)

// Terminals lists terminals found on this machine.
func Terminals() []string {
	out := []string{"Terminal"}
	if _, err := os.Stat("/Applications/iTerm.app"); err == nil {
		out = append(out, "iTerm")
	}
	return out
}

func appleString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// Terminal opens a new terminal window running argv.
func Terminal(preferred string, argv []string) error {
	cmd := shellJoin(argv)
	if preferred == "iTerm" {
		if _, err := os.Stat("/Applications/iTerm.app"); err == nil {
			return start("osascript",
				"-e", `tell application "iTerm" to create window with default profile command `+appleString(cmd),
				"-e", `tell application "iTerm" to activate`)
		}
	}
	return start("osascript",
		"-e", `tell application "Terminal" to do script `+appleString(cmd),
		"-e", `tell application "Terminal" to activate`)
}
