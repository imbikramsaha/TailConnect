// Package store persists TailConnect's settings, devices, custom apps and
// trusted peers in a single JSON file with atomic writes.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Device kinds.
const (
	KindPeer = "peer" // another device running TailConnect on the tailnet
	KindSSH  = "ssh"  // any SSH host (LAN, internet, or tailnet without TailConnect)
)

// Ways to reach an SSH device.
const (
	ViaDirect  = "direct"  // plain TCP from this machine (LAN / public host)
	ViaTailnet = "tailnet" // through the embedded Tailscale node
)

// Settings are user preferences.
type Settings struct {
	// AutoTrustOwnDevices pairs automatically with TailConnect devices signed
	// in to the same Tailscale account (both directions), so two devices are
	// "synced" with zero clicks.
	AutoTrustOwnDevices bool `json:"autoTrustOwnDevices"`
	// StartAtLogin registers the app to launch on login.
	StartAtLogin bool `json:"startAtLogin"`
	// Terminal is the preferred terminal ("" = auto-detect).
	Terminal string `json:"terminal"`
	// EnabledApps lists built-in launcher IDs shown in the tray/UI.
	EnabledApps []string `json:"enabledApps"`
	// ControlURL is a custom coordination server (e.g. Headscale). "" = Tailscale.
	ControlURL string `json:"controlURL"`
	// Hostname overrides this device's tailnet hostname.
	Hostname string `json:"hostname"`
	// ManageSSHConfig adds an Include for TailConnect hosts to ~/.ssh/config.
	ManageSSHConfig bool `json:"manageSSHConfig"`
	// LocalSSHPort is where this machine's SSH server listens (for peers).
	LocalSSHPort int `json:"localSSHPort"`
	// Theme is "system", "light" or "dark".
	Theme string `json:"theme"`
	// Onboarded is set once the welcome flow is finished.
	Onboarded bool `json:"onboarded"`
}

// Device is a saved device. Runtime state (online, links) is not persisted.
type Device struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Alias  string `json:"alias"` // ssh alias, e.g. "tc-desktop"
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
	User   string `json:"user,omitempty"`
	Via    string `json:"via,omitempty"`
	NodeID string `json:"nodeID,omitempty"` // Tailscale StableNodeID (peers)
	OS     string `json:"os,omitempty"`
	Home   string `json:"home,omitempty"` // remote home dir, for IDE folders
	// Imported devices come from the user's ~/.ssh/config and are launched
	// through the system ssh client using their existing alias.
	Imported bool      `json:"imported,omitempty"`
	AddedAt  time.Time `json:"addedAt"`
}

// App is a custom app: a remote port forwarded to localhost, optionally
// started by a launch script, opened in a browser, or run in a terminal.
type App struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Port        int    `json:"port"`
	Script      string `json:"script,omitempty"`
	OpenBrowser bool   `json:"openBrowser"`
	Path        string `json:"path,omitempty"`
	InTerminal  bool   `json:"inTerminal"`
}

// TrustedPeer is a device allowed to access this one (stats, tunnels, SSH key).
type TrustedPeer struct {
	NodeID    string    `json:"nodeID"`
	Name      string    `json:"name"`
	LoginName string    `json:"loginName,omitempty"`
	PublicKey string    `json:"publicKey,omitempty"`
	TrustedAt time.Time `json:"trustedAt"`
}

// State is everything persisted.
type State struct {
	Version  int           `json:"version"`
	Settings Settings      `json:"settings"`
	Devices  []Device      `json:"devices"`
	Apps     []App         `json:"apps"`
	Trusted  []TrustedPeer `json:"trusted"`
	// Ignored lists tailnet node IDs the user removed; they are not auto-paired again.
	Ignored []string `json:"ignored,omitempty"`
}

// DefaultApps are the built-in launchers enabled on first run.
var DefaultApps = []string{"terminal", "vscode", "cursor"}

func defaults() State {
	return State{
		Version: 1,
		Settings: Settings{
			AutoTrustOwnDevices: true,
			EnabledApps:         slices.Clone(DefaultApps),
			ManageSSHConfig:     true,
			LocalSSHPort:        22,
			Theme:               "system",
		},
		Apps: []App{
			{ID: "jupyter", Name: "JupyterLab", Port: 8888, OpenBrowser: true,
				Script: "jupyter lab --no-browser --port=8888 --ServerApp.token='' --ServerApp.password=''"},
		},
	}
}

// Store is a concurrency-safe handle to the state file.
type Store struct {
	mu   sync.Mutex
	path string
	st   State
}

// Open loads (or initialises) the state at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, st: defaults()}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, s.saveLocked()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, err
	}
	if s.st.Settings.LocalSSHPort == 0 {
		s.st.Settings.LocalSSHPort = 22
	}
	return s, nil
}

// Get returns a deep copy of the current state.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.st)
}

// Update applies fn to a copy of the state and persists it if fn succeeds.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.st)
	if err := fn(&next); err != nil {
		return err
	}
	prev := s.st
	s.st = next
	if err := s.saveLocked(); err != nil {
		s.st = prev
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, b, 0o600)
}

func clone(st State) State {
	st.Settings.EnabledApps = slices.Clone(st.Settings.EnabledApps)
	st.Devices = slices.Clone(st.Devices)
	st.Apps = slices.Clone(st.Apps)
	st.Trusted = slices.Clone(st.Trusted)
	st.Ignored = slices.Clone(st.Ignored)
	return st
}

// WriteFileAtomic writes data to a temp file and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FindDevice returns the index of the device with id, or -1.
func (st State) FindDevice(id string) int {
	return slices.IndexFunc(st.Devices, func(d Device) bool { return d.ID == id })
}

// FindDeviceByNode returns the index of the peer device with nodeID, or -1.
func (st State) FindDeviceByNode(nodeID string) int {
	return slices.IndexFunc(st.Devices, func(d Device) bool { return d.NodeID != "" && d.NodeID == nodeID })
}

// FindApp returns the index of the app with id, or -1.
func (st State) FindApp(id string) int {
	return slices.IndexFunc(st.Apps, func(a App) bool { return a.ID == id })
}

// IsTrusted reports whether nodeID may access this device.
func (st State) IsTrusted(nodeID string) bool {
	return nodeID != "" && slices.ContainsFunc(st.Trusted, func(t TrustedPeer) bool { return t.NodeID == nodeID })
}
