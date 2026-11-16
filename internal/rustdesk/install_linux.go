package rustdesk

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func install(ctx context.Context, pkg string) error {
	var cmd *exec.Cmd
	switch {
	case strings.HasSuffix(pkg, ".deb"):
		cmd = exec.CommandContext(ctx, "apt-get", "install", "-y", pkg)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	case strings.HasSuffix(pkg, "-suse.rpm"):
		cmd = exec.CommandContext(ctx, "zypper", "--non-interactive", "install", "--allow-unsigned-rpm", pkg)
	default:
		if _, err := exec.LookPath("dnf"); err == nil {
			cmd = exec.CommandContext(ctx, "dnf", "install", "-y", pkg)
		} else {
			cmd = exec.CommandContext(ctx, "yum", "install", "-y", pkg)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return fmt.Errorf("installing RustDesk: %s", lines[len(lines)-1])
	}
	return nil
}

func startInSession(int) {}

// repairUserConfig is only needed on macOS.
func repairUserConfig(int, bool) bool { return false }

// verifyInstalled: RustDesk is installed where only administrators can
// change it.
func verifyInstalled(context.Context) error { return nil }

func stopInSession(int) {}
