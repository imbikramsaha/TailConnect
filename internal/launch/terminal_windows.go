package launch

import (
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

const createNewConsole = 0x00000010

// Terminals lists terminals found on this machine.
func Terminals() []string {
	out := []string{}
	if _, err := exec.LookPath("wt.exe"); err == nil {
		out = append(out, "Windows Terminal")
	}
	out = append(out, "PowerShell", "Command Prompt")
	return out
}

// Terminal opens a new terminal window running argv. No cmd.exe is
// involved, so nothing in argv is ever interpreted by a shell here.
func Terminal(preferred string, argv []string) error {
	if len(argv) == 0 {
		return exec.ErrNotFound
	}
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
		// -EncodedCommand sidesteps PowerShell's command-line re-parsing;
		// each argument is a single-quoted literal inside the script.
		return startConsole("powershell.exe", "-NoLogo", "-NoExit", "-EncodedCommand", psEncoded(argv))
	}
	bin, err := exec.LookPath(argv[0]) // never resolves to the current directory
	if err != nil {
		return err
	}
	return startConsole(bin, argv[1:]...)
}

// startConsole runs a program in a new console window.
func startConsole(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole | createNewProcessGroup}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// psEncoded returns argv as a PowerShell "& 'cmd' 'arg' ..." script,
// base64-encoded UTF-16LE for -EncodedCommand.
func psEncoded(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
	}
	u := utf16.Encode([]rune("& " + strings.Join(q, " ")))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}
