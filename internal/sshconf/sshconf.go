// Package sshconf generates the OpenSSH config TailConnect manages so the
// system ssh client, VS Code, Cursor, Zed, etc. can reach devices by alias
// (e.g. `ssh tc-desktop`), and wires it into ~/.ssh/config with an Include.
package sshconf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/imbikramsaha/tailconnect/internal/store"
)

// Entry is one Host block.
type Entry struct {
	Alias    string
	HostName string
	Port     int
	User     string
	DeviceID string
	// Proxy routes the connection through `tailconnect proxy` (tailnet peers
	// and tailnet SSH hosts, which the OS cannot route to directly).
	Proxy bool
}

// Options are shared settings for every entry.
type Options struct {
	Exe        string // path to the tailconnect binary
	KeyPath    string
	KnownHosts string
}

// cfgPath formats a filesystem path for ssh_config (forward slashes, quoted).
func cfgPath(p string) string {
	if runtime.GOOS == "windows" {
		p = filepath.ToSlash(p)
	}
	return `"` + p + `"`
}

// Render produces the managed config file contents.
func Render(entries []Entry, o Options) string {
	var b strings.Builder
	b.WriteString("# Managed by TailConnect. Changes are overwritten; edit devices in the app.\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "Host %s\n", e.Alias)
		host := e.HostName
		if e.Proxy || host == "" {
			host = e.Alias
		}
		fmt.Fprintf(&b, "  HostName %s\n", host)
		if e.User != "" {
			fmt.Fprintf(&b, "  User %s\n", e.User)
		}
		port := e.Port
		if port == 0 {
			port = 22
		}
		fmt.Fprintf(&b, "  Port %d\n", port)
		fmt.Fprintf(&b, "  IdentityFile %s\n", cfgPath(o.KeyPath))
		b.WriteString("  IdentitiesOnly yes\n")
		fmt.Fprintf(&b, "  HostKeyAlias %s\n", e.Alias)
		fmt.Fprintf(&b, "  UserKnownHostsFile %s\n", cfgPath(o.KnownHosts))
		b.WriteString("  StrictHostKeyChecking accept-new\n")
		b.WriteString("  ServerAliveInterval 15\n")
		if e.Proxy {
			fmt.Fprintf(&b, "  ProxyCommand %s proxy %s %%p\n", cfgPath(o.Exe), e.DeviceID)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Write writes the managed config if its contents changed.
func Write(path, content string) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
	}
	return store.WriteFileAtomic(path, []byte(content), 0o600)
}

// IncludeLine is the line added to ~/.ssh/config.
func IncludeLine(managedPath string) string {
	return "Include " + cfgPath(managedPath)
}

// EnsureInclude makes sure userConfig starts with an Include of managedPath.
// OpenSSH only honours Include before the first Host block for global scope,
// so the line is prepended. A one-time backup is kept as config.tailconnect.bak.
func EnsureInclude(userConfig, managedPath string) error {
	line := IncludeLine(managedPath)
	old, err := os.ReadFile(userConfig)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(old), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(old) > 0 {
		bak := userConfig + ".tailconnect.bak"
		if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(bak, old, 0o600); err != nil {
				return err
			}
		}
	}
	var buf bytes.Buffer
	buf.WriteString("# Added by TailConnect: hosts for your TailConnect devices.\n")
	buf.WriteString(line + "\n\n")
	buf.Write(old)
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		return err
	}
	return store.WriteFileAtomic(userConfig, buf.Bytes(), 0o600)
}

// RemoveInclude strips TailConnect's Include from userConfig.
func RemoveInclude(userConfig, managedPath string) error {
	old, err := os.ReadFile(userConfig)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	line := IncludeLine(managedPath)
	var out []string
	for _, l := range strings.Split(string(old), "\n") {
		t := strings.TrimSpace(l)
		if t == line || t == "# Added by TailConnect: hosts for your TailConnect devices." {
			continue
		}
		out = append(out, l)
	}
	return store.WriteFileAtomic(userConfig, []byte(strings.TrimLeft(strings.Join(out, "\n"), "\n")), 0o600)
}

// Alias builds a safe ssh alias from a device name, e.g. "My Desktop" -> "tc-my-desktop".
func Alias(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r == '\'' || r == '’' || r == '`':
			continue // "Bikram's Mac" -> "bikrams-mac"
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "device"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return "tc-" + s
}
