package rustdesk

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// elevate asks through polkit (the desktop's administrator prompt).
func elevate(ctx context.Context, exe, spec string) error {
	pk, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("install RustDesk with your package manager, or run: sudo " + exe + " rustdesk-admin <spec> (pkexec isn't available)")
	}
	out, err := exec.CommandContext(ctx, pk, exe, "rustdesk-admin", spec).CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			return fmt.Errorf("cancelled")
		}
		return fmt.Errorf("administrator step failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
