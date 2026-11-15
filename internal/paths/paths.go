// Package paths resolves the per-user directories TailConnect uses.
//
// Everything lives under one config directory (overridable with
// TAILCONNECT_HOME so several instances can run side by side for testing):
//
//	<config>/state.json     persisted settings, devices, custom apps, trust list
//	<config>/control.json   local control API port + token (0600)
//	<config>/tsnet/         embedded Tailscale node state
//	<config>/logs/          app + session logs
//
// SSH material lives in ~/.ssh/tailconnect/ so OpenSSH and editors find it.
package paths

import (
	"os"
	"path/filepath"
)

// EnvHome overrides the config directory (and isolates the SSH directory).
const EnvHome = "TAILCONNECT_HOME"

func ensure(dir string, perm os.FileMode) (string, error) {
	if err := os.MkdirAll(dir, perm); err != nil {
		return "", err
	}
	return dir, nil
}

// ConfigDir returns the TailConnect config directory, creating it.
func ConfigDir() (string, error) {
	if d := os.Getenv(EnvHome); d != "" {
		return ensure(d, 0o700)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(base, "TailConnect"), 0o700)
}

func sub(name string) (string, error) {
	c, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(c, name), 0o700)
}

// TSNetDir holds the embedded Tailscale node's state.
func TSNetDir() (string, error) { return sub("tsnet") }

// LogDir holds log files.
func LogDir() (string, error) { return sub("logs") }

// SSHDir holds TailConnect's SSH key, known_hosts and generated ssh config.
func SSHDir() (string, error) {
	if os.Getenv(EnvHome) != "" {
		return sub("ssh")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(home, ".ssh", "tailconnect"), 0o700)
}

// UserSSHDir is the user's ~/.ssh directory.
func UserSSHDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh"), nil
}

// ManagesUserSSHConfig reports whether TailConnect should add its Include
// line to ~/.ssh/config. Disabled for isolated (TAILCONNECT_HOME) instances.
func ManagesUserSSHConfig() bool { return os.Getenv(EnvHome) == "" }

// StateFile is the persisted state path.
func StateFile() (string, error) {
	c, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "state.json"), nil
}

// ControlFile is where the running instance advertises its control API.
func ControlFile() (string, error) {
	c, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "control.json"), nil
}
