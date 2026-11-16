package rustdesk

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// elevate shows Windows' UAC prompt and waits for the elevated step.
func elevate(ctx context.Context, exe, spec string) error {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	// Start-Process joins -ArgumentList with spaces: quote the path itself.
	script := "$p = Start-Process -FilePath " + q(exe) + " -ArgumentList 'rustdesk-admin'," + q(`"`+spec+`"`) +
		" -Verb RunAs -WindowStyle Hidden -PassThru; $null = $p.Handle; $p.WaitForExit(); exit $p.ExitCode"
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "canceled by the user") || strings.Contains(string(out), "cancelled by the user") {
			return fmt.Errorf("cancelled")
		}
		return fmt.Errorf("administrator step failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
