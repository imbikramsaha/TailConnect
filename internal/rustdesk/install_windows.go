package rustdesk

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func install(ctx context.Context, exe string) error {
	if err := verifySigned(ctx, exe); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "--silent-install")
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait for the installer process only (it starts RustDesk's service).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("RustDesk's installer failed: %w", err)
		}
	case <-time.After(5 * time.Minute):
		return errors.New("RustDesk's installer didn't finish")
	}
	for i := 0; i < 90 && Bin() == ""; i++ {
		time.Sleep(time.Second)
	}
	if Bin() == "" {
		return errors.New("RustDesk didn't install")
	}
	time.Sleep(5 * time.Second) // let its service start
	return nil
}

// verifySigned checks the installer's Authenticode signature is valid and
// RustDesk's (PURSLANE).
func verifySigned(ctx context.Context, p string) error {
	script := "$s = Get-AuthenticodeSignature -LiteralPath '" + strings.ReplaceAll(p, "'", "''") + "'; \"$($s.Status)|$($s.SignerCertificate.Subject)\""
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return fmt.Errorf("checking RustDesk's signature: %w", err)
	}
	status, subject, _ := strings.Cut(strings.TrimSpace(string(out)), "|")
	if status != "Valid" || !strings.HasPrefix(subject, winSignerCN+",") {
		return errors.New("RustDesk's installer isn't signed by RustDesk's developer; not installing it")
	}
	return nil
}

func startInSession(int) {}

// repairUserConfig is only needed on macOS.
func repairUserConfig(int) {}
