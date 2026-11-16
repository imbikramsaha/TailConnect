package rustdesk

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// elevate shows macOS's standard administrator dialog.
func elevate(ctx context.Context, exe, spec string) error {
	sh := shQuote(exe) + " rustdesk-admin " + shQuote(spec)
	script := `do shell script "` + asEscape(sh) + `" with prompt "TailConnect needs to install and set up RustDesk for screen sharing." with administrator privileges`
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "-128") {
			return fmt.Errorf("cancelled")
		}
		return fmt.Errorf("administrator step failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func asEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}
