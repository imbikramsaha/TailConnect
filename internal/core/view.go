package core

import (
	"context"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
)

// Device sections in the UI.
const (
	SectionMine       = "mine"       // saved devices
	SectionDiscovered = "discovered" // TailConnect devices not yet paired
	SectionTailnet    = "tailnet"    // other tailnet nodes (no TailConnect)
)

// DeviceView is a device as presented to the UI and tray.
type DeviceView struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Section     string    `json:"section"`
	Name        string    `json:"name"`
	Alias       string    `json:"alias,omitempty"`
	OS          string    `json:"os,omitempty"`
	Host        string    `json:"host,omitempty"`
	Port        int       `json:"port,omitempty"`
	User        string    `json:"user,omitempty"`
	Via         string    `json:"via,omitempty"`
	Home        string    `json:"home,omitempty"`
	TailnetIP   string    `json:"tailnetIP,omitempty"`
	DNSName     string    `json:"dnsName,omitempty"`
	LoginName   string    `json:"loginName,omitempty"`
	Online      bool      `json:"online"`
	TailConnect bool      `json:"tailconnect"`
	Version     string    `json:"version,omitempty"`
	SSH         bool      `json:"ssh"`
	TrustsUs    bool      `json:"trustsUs"`
	TrustedByUs bool      `json:"trustedByUs"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	Link        string    `json:"link,omitempty"`
	Pairing     string    `json:"pairing,omitempty"`
	PairingMsg  string    `json:"pairingMsg,omitempty"`
	Running     []running `json:"running"`
}

// BuiltinView is a built-in launcher.
type BuiltinView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
}

// PairRequestView is a pending incoming pairing request.
type PairRequestView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	NodeName  string    `json:"nodeName"`
	LoginName string    `json:"loginName"`
	OS        string    `json:"os"`
	SameUser  bool      `json:"sameUser"`
	Created   time.Time `json:"created"`
}

// SelfView describes this device.
type SelfView struct {
	Name       string `json:"name"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	User       string `json:"user"`
	SSH        bool   `json:"ssh"`
	SSHPort    int    `json:"sshPort"`
	PublicKey  string `json:"publicKey"`
	SSHConfig  string `json:"sshConfig"`
	ConfigPath string `json:"configPath,omitempty"`
}

// View is the full UI state.
type View struct {
	Version         string              `json:"version"`
	Tailnet         tailnet.Info        `json:"tailnet"`
	Self            SelfView            `json:"self"`
	Devices         []DeviceView        `json:"devices"`
	Apps            []store.App         `json:"apps"`
	Builtins        []BuiltinView       `json:"builtins"`
	PairRequests    []PairRequestView   `json:"pairRequests"`
	Trusted         []store.TrustedPeer `json:"trusted"`
	Settings        store.Settings      `json:"settings"`
	Terminals       []string            `json:"terminals"`
	RestartRequired bool                `json:"restartRequired"`
	Headless        bool                `json:"headless"`
	LogDir          string              `json:"logDir,omitempty"`
	InstallWarning  string              `json:"installWarning,omitempty"`
}

var (
	builtinMu    sync.Mutex
	builtinCache map[string]bool
	builtinAt    time.Time
)

func builtinAvailability() map[string]bool {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if builtinCache != nil && time.Since(builtinAt) < 30*time.Second {
		return builtinCache
	}
	m := map[string]bool{}
	for _, b := range launch.Builtins {
		m[b.ID] = b.Available()
	}
	builtinCache, builtinAt = m, time.Now()
	return m
}

// Snapshot builds the current View.
func (c *Core) Snapshot(ctx context.Context) View {
	st := c.store.Get()
	v := View{
		Version:         c.opts.Version,
		Tailnet:         c.node.Info(ctx),
		Apps:            st.Apps,
		Trusted:         st.Trusted,
		Settings:        st.Settings,
		RestartRequired: c.RestartRequired(),
		Headless:        c.opts.Headless,
		InstallWarning:  c.opts.InstallWarning,
		Self: SelfView{
			Name: c.selfName, OS: runtime.GOOS, Arch: runtime.GOARCH, User: c.osUser,
			SSH: netx.PortOpen(st.Settings.LocalSSHPort), SSHPort: st.Settings.LocalSSHPort,
			PublicKey: c.id.PublicLine, SSHConfig: c.managedConfigPath(),
		},
	}
	if c.logs != nil {
		v.LogDir = c.logs.Dir
	}
	if !c.opts.Headless {
		v.Terminals = launch.Terminals()
	}
	avail := builtinAvailability()
	for _, b := range launch.Builtins {
		v.Builtins = append(v.Builtins, BuiltinView{
			ID: b.ID, Name: b.Name, Available: avail[b.ID],
			Enabled: slices.Contains(st.Settings.EnabledApps, b.ID),
		})
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	saved := map[string]bool{}
	for _, d := range st.Devices {
		dv := DeviceView{
			ID: d.ID, Kind: d.Kind, Section: SectionMine, Name: d.Name, Alias: d.Alias, OS: d.OS,
			Host: d.Host, Port: d.Port, User: d.User, Via: d.Via, Home: d.Home, State: StateDisconnected,
			Running: []running{},
		}
		switch d.Kind {
		case store.KindPeer:
			saved[d.NodeID] = true
			dv.TailConnect = true
			dv.TrustedByUs = st.IsTrusted(d.NodeID)
			if p := c.peers[d.NodeID]; p != nil {
				dv.Online, dv.TailnetIP, dv.DNSName, dv.Link, dv.LoginName = p.Online, p.IP, p.DNSName, p.Link, p.LoginName
				if p.Hello != nil {
					dv.SSH, dv.TrustsUs, dv.Version = p.Hello.SSH, p.Hello.Trusted, p.Hello.Version
				}
			}
			if o := c.outgoing[d.NodeID]; o != nil && o.Status != outDone {
				dv.Pairing, dv.PairingMsg = o.Status, o.Message
			}
		case store.KindSSH:
			dv.SSH = true
			dv.Online = true // unknown until connected; treated as reachable
			if d.Via == store.ViaTailnet {
				dv.Link = "Tailscale"
				for _, p := range c.peers {
					if d.Host == p.IP || d.Host == p.HostName || d.Host == p.DNSName || d.Host == shortName(p.DNSName) {
						dv.Online, dv.TailnetIP, dv.Link = p.Online, p.IP, p.Link
						break
					}
				}
			} else {
				dv.Link = "Direct"
			}
		}
		if cs := c.conns[d.ID]; cs != nil {
			dv.State, dv.Error, dv.Since = cs.State, cs.Err, cs.Since
			for _, r := range cs.apps {
				dv.Running = append(dv.Running, *r)
			}
			sort.Slice(dv.Running, func(i, j int) bool { return dv.Running[i].Name < dv.Running[j].Name })
		}
		v.Devices = append(v.Devices, dv)
	}
	for id, p := range c.peers {
		if saved[id] {
			continue
		}
		dv := DeviceView{
			ID: "p-" + id, Kind: store.KindPeer, Name: displayName(*p), OS: p.OS, TailnetIP: p.IP,
			DNSName: p.DNSName, LoginName: p.LoginName, Online: p.Online, Link: p.Link,
			State: StateDisconnected, Running: []running{}, TrustedByUs: st.IsTrusted(id),
		}
		if p.Hello != nil {
			dv.Section = SectionDiscovered
			dv.TailConnect = true
			dv.OS = p.Hello.OS
			dv.SSH, dv.TrustsUs, dv.Version = p.Hello.SSH, p.Hello.Trusted, p.Hello.Version
			if o := c.outgoing[id]; o != nil && o.Status != outDone {
				dv.Pairing, dv.PairingMsg = o.Status, o.Message
			}
		} else {
			dv.Section = SectionTailnet
			dv.ID = "n-" + id
			dv.Host = p.IP
		}
		v.Devices = append(v.Devices, dv)
	}
	rank := map[string]int{SectionMine: 0, SectionDiscovered: 1, SectionTailnet: 2}
	sort.SliceStable(v.Devices, func(i, j int) bool {
		a, b := v.Devices[i], v.Devices[j]
		if rank[a.Section] != rank[b.Section] {
			return rank[a.Section] < rank[b.Section]
		}
		if a.Online != b.Online {
			return a.Online
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for _, in := range c.incoming {
		if in.Status != "pending" {
			continue
		}
		v.PairRequests = append(v.PairRequests, PairRequestView{
			ID: in.ID, Name: in.Req.Name, NodeName: in.Caller.Name, LoginName: in.Caller.LoginName,
			OS: in.Req.OS, SameUser: in.Caller.UserID != 0 && in.Caller.UserID == c.selfUserID, Created: in.Created,
		})
	}
	sort.Slice(v.PairRequests, func(i, j int) bool { return v.PairRequests[i].Created.Before(v.PairRequests[j].Created) })
	if v.Devices == nil {
		v.Devices = []DeviceView{}
	}
	if v.PairRequests == nil {
		v.PairRequests = []PairRequestView{}
	}
	if v.Trusted == nil {
		v.Trusted = []store.TrustedPeer{}
	}
	if v.Apps == nil {
		v.Apps = []store.App{}
	}
	return v
}
