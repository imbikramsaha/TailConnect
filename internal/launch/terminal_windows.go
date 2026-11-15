package launch

import (
	"os/exec"
	"strings"
)

// Terminals lists terminals found on this machine.
func Terminals() []string {
	out := []string{}
	if _, err := exec.LookPath("wt.exe"); err == nil {
		out = append(out, "Windows Terminal")
	}
	out = append(out, "PowerShell", "Command Prompt")
	return out
}

// Terminal opens a new terminal window running argv.
func Terminal(preferred string, argv []string) error {
	if preferred == "" || preferred == "Windows Terminal" {
		if wt, err := exec.LookPath("wt.exe"); err == nil {
			// wt treats a bare ";" as its own command separator.
			esc := make([]string, len(argv))
			for i, a := range argv {
				esc[i] = strings.ReplaceAll(a, ";", `\;`)
			}
			return startVisible(wt, append([]string{"-w", "new"}, esc...)...)
		}
	}
	if preferred == "PowerShell" {
		return startVisible("cmd.exe", append([]string{"/c", "start", "TailConnect", "powershell.exe", "-NoExit", "-Command"}, argv...)...)
	}
	return startVisible("cmd.exe", append([]string{"/c", "start", "TailConnect"}, argv...)...)
}
