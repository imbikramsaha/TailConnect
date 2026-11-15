// Package autostart registers TailConnect to start when the user logs in.
package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const appName = "TailConnect"

// Set enables or disables start-at-login for exe.
func Set(enabled bool, exe string) error {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		return setXDG(enabled, exe)
	case "darwin":
		return setLaunchAgent(enabled, exe)
	case "windows":
		return setWindows(enabled, exe)
	}
	return fmt.Errorf("start at login is not supported on %s", runtime.GOOS)
}

func xdgPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "tailconnect.desktop"), nil
}

func setXDG(enabled bool, exe string) error {
	p, err := xdgPath()
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=Connect to your devices over Tailscale
Exec="%s" --background
Icon=tailconnect
Terminal=false
X-GNOME-Autostart-enabled=true
`, appName, strings.ReplaceAll(exe, `"`, `\"`))
	return os.WriteFile(p, []byte(content), 0o644)
}

func setLaunchAgent(enabled bool, exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "com.tailconnect.app.plist")
	if !enabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(exe)
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.tailconnect.app</string>
  <key>ProgramArguments</key>
  <array><string>` + esc + `</string><string>--background</string></array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`
	return os.WriteFile(p, []byte(content), 0o644)
}
