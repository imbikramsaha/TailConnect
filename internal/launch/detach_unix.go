//go:build !windows

package launch

import (
	"os/exec"
	"syscall"
)

// detach starts the child in its own session so it outlives TailConnect.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
