package launch

import (
	"os/exec"
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
			return startVisible(wt, append([]string{"-w", "new"}, argv...)...)
		}
	}
	if preferred == "PowerShell" {
		return startVisible("cmd.exe", append([]string{"/c", "start", "TailConnect", "powershell.exe", "-NoExit", "-Command"}, argv...)...)
	}
	return startVisible("cmd.exe", append([]string{"/c", "start", "TailConnect"}, argv...)...)
}
