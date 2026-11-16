// Package store persists TailConnect's settings, devices, custom apps and
// trusted peers in a single JSON file with atomic writes.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// BuiltinSSH serves SSH to paired devices from TailConnect itself, so
	// terminals and editors work without enabling an SSH server.
	BuiltinSSH bool `json:"builtinSSH"`
	// OwnDeviceAccess is the access level granted to auto-paired devices on
	// the same Tailscale account.
	OwnDeviceAccess string `json:"ownDeviceAccess"`
	// Notifications shows system notifications.
	Notifications bool `json:"notifications"`
	// RustDeskLock limits RustDesk (when TailConnect set it up) to
	// connections through TailConnect; off also allows RustDesk's own IDs.
	RustDeskLock bool `json:"rustdeskLock"`
	// CheckUpdates looks for new releases daily.
	CheckUpdates bool `json:"checkUpdates"`
	// Alerts watches connected devices' resources.
	Alerts Alerts `json:"alerts"`
	// TailscaleMode is "embedded" (TailConnect's own Tailscale node), "app"
	// (use the Tailscale app on this computer) or "" (decide at first run).
	TailscaleMode string `json:"tailscaleMode,omitempty"`
}

// Alerts are resource thresholds (percent / °C); 0 disables one.
type Alerts struct {
	Enabled  bool    `json:"enabled"`
	GPUTempC float64 `json:"gpuTempC"`
	DiskPct  float64 `json:"diskPct"`
	MemPct   float64 `json:"memPct"`
}

// Access levels a device can be granted on this one.
const (
	AccessFull  = "full"  // terminal, editors, files, ports, stats
	AccessPorts = "ports" // forwarded ports, remote desktop, stats
	AccessStats = "stats" // resource monitor only
)

// AccessRank orders access levels (unknown/empty = full, for old state).
func AccessRank(a string) int {
	switch a {
	case AccessStats:
		return 1
	case AccessPorts:
		return 2
	default:
		return 3
	}
}

// ValidAccess reports whether a is a known access level.
func ValidAccess(a string) bool { return a == AccessFull || a == AccessPorts || a == AccessStats }

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
	// Wake-on-LAN details, remembered while the device was online.
	MACs []string `json:"macs,omitempty"`
	LANs []string `json:"lans,omitempty"` // CIDRs, e.g. 192.168.1.0/24
	// PeerKey is the TailConnect device key a peer signed its answers with
	// (pinned on first signed contact). Once set, answers not signed with
	// it — e.g. another program on that machine posing as TailConnect —
	// are refused.
	PeerKey string `json:"peerKey,omitempty"`
	// Screen is the preferred way to view this device's screen:
	// "" (automatic), "rustdesk" or "system".
	Screen string `json:"screen,omitempty"`
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
	// Devices limits the app to these device IDs (empty = every device).
	Devices []string `json:"devices,omitempty"`
}

// AppliesTo reports whether the app is offered on device id.
func (a App) AppliesTo(id string) bool { return len(a.Devices) == 0 || slices.Contains(a.Devices, id) }

// TrustedPeer is a device allowed to access this one (stats, tunnels, SSH key).
type TrustedPeer struct {
	NodeID    string    `json:"nodeID"`
	Name      string    `json:"name"`
	LoginName string    `json:"loginName,omitempty"`
	PublicKey string    `json:"publicKey,omitempty"`
	TrustedAt time.Time `json:"trustedAt"`
	// Access is the granted level (see Access*; "" = full for old state).
	Access string `json:"access,omitempty"`
	// Ports, if non-empty, limits which local ports the device may tunnel to.
	Ports []int `json:"ports,omitempty"`
}

// Level returns the effective access level.
func (t TrustedPeer) Level() string {
	if ValidAccess(t.Access) {
		return t.Access
	}
	return AccessStats // unknown values fail closed (old state is migrated to "full")
}

// Pending is a pairing request awaiting the user's decision (persisted so a
// restart doesn't lose it).
type Pending struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"nodeID"`
	NodeName  string    `json:"nodeName"`
	LoginName string    `json:"loginName"`
	UserID    int64     `json:"userID"`
	Name      string    `json:"name"`
	OS        string    `json:"os"`
	PublicKey string    `json:"publicKey"`
	Code      string    `json:"code"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created"`
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
	// Pending are pairing requests made to this device.
	Pending []Pending `json:"pending,omitempty"`
	// RustDesk is TailConnect's setup of RustDesk on this computer.
	RustDesk RustDeskSetup `json:"rustdesk,omitempty"`
}

// RustDeskSetup records how TailConnect configured RustDesk here.
type RustDeskSetup struct {
	// Password is the random permanent RustDesk password TailConnect set.
	// It is shared only with paired devices allowed to share this screen.
	Password string `json:"password,omitempty"`
	// Configured: TailConnect set RustDesk up (direct access on). Password
	// stays empty if RustDesk refused it (then people type RustDesk's own).
	Configured bool `json:"configured,omitempty"`
	// Locked: RustDesk only accepts connections through TailConnect.
	Locked       bool      `json:"locked,omitempty"`
	Version      string    `json:"version,omitempty"`
	ConfiguredAt time.Time `json:"configuredAt,omitempty"`
}

// stateVersion is the current schema version.
const stateVersion = 3

// DefaultApps are the built-in launchers enabled on first run.
var DefaultApps = []string{"terminal", "vscode", "cursor"}

func defaults() State {
	return State{
		Version: stateVersion,
		Settings: Settings{
			AutoTrustOwnDevices: true,
			EnabledApps:         slices.Clone(DefaultApps),
			ManageSSHConfig:     true,
			LocalSSHPort:        22,
			Theme:               "system",
			BuiltinSSH:          true,
			OwnDeviceAccess:     AccessFull,
			Notifications:       true,
			RustDeskLock:        true,
			CheckUpdates:        true,
			Alerts:              DefaultAlerts(),
		},
		Apps: []App{defaultJupyter()},
	}
}

// defaultJupyter runs JupyterLab with a fresh random token per start
// ($TC_APP_TOKEN), opened via {token} in the URL path — never without auth,
// since anyone with access to the device's localhost could use it.
func defaultJupyter() App {
	return App{ID: "jupyter", Name: "JupyterLab", Port: 8888, OpenBrowser: true, Path: "lab?token={token}",
		Script: `jupyter lab --no-browser --port=8888 --IdentityProvider.token="$TC_APP_TOKEN"`}
}

// DefaultAlerts are the default resource alert thresholds.
func DefaultAlerts() Alerts {
	return Alerts{Enabled: true, GPUTempC: 85, DiskPct: 95, MemPct: 95}
}

// migrate upgrades state written by older versions.
func migrate(st *State) {
	if st.Version < 2 {
		st.Settings.BuiltinSSH = true
		st.Settings.OwnDeviceAccess = AccessFull
		st.Settings.Notifications = true
		st.Settings.CheckUpdates = true
		st.Settings.Alerts = DefaultAlerts()
		st.Version = 2
	}
	if st.Version < 3 {
		// v1/v2 stored "" for full access.
		for i := range st.Trusted {
			if st.Trusted[i].Access == "" {
				st.Trusted[i].Access = AccessFull
			}
		}
		// The old default JupyterLab app ran without a token.
		for i := range st.Apps {
			if st.Apps[i].ID == "jupyter" && strings.Contains(st.Apps[i].Script, "--ServerApp.token=''") {
				st.Apps[i] = defaultJupyter()
			}
		}
		st.Version = 3
	}
	if !ValidAccess(st.Settings.OwnDeviceAccess) {
		st.Settings.OwnDeviceAccess = AccessFull
	}
	if st.Settings.LocalSSHPort == 0 {
		st.Settings.LocalSSHPort = 22
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
	s.st = State{} // fields absent from the file stay zero; migrate fills them
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, err
	}
	before := s.st.Version
	migrate(&s.st)
	if before != s.st.Version {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
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
	st.Pending = slices.Clone(st.Pending)
	for i := range st.Trusted {
		st.Trusted[i].Ports = slices.Clone(st.Trusted[i].Ports)
	}
	for i := range st.Apps {
		st.Apps[i].Devices = slices.Clone(st.Apps[i].Devices)
	}
	for i := range st.Devices {
		st.Devices[i].MACs = slices.Clone(st.Devices[i].MACs)
		st.Devices[i].LANs = slices.Clone(st.Devices[i].LANs)
	}
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

// Trust returns the trusted entry for nodeID.
func (st *State) Trust(nodeID string) (TrustedPeer, bool) {
	for _, t := range st.Trusted {
		if nodeID != "" && t.NodeID == nodeID {
			return t, true
		}
	}
	return TrustedPeer{}, false
}

// IsTrusted reports whether nodeID may access this device.
func (st State) IsTrusted(nodeID string) bool {
	return nodeID != "" && slices.ContainsFunc(st.Trusted, func(t TrustedPeer) bool { return t.NodeID == nodeID })
}
