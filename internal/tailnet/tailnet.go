// Package tailnet wraps an embedded Tailscale node (tsnet) so TailConnect
// joins the user's tailnet without a separately installed Tailscale client.
package tailnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

// Backend states (mirrors ipn.State strings).
const (
	StateNoState    = "NoState"
	StateNeedsLogin = "NeedsLogin"
	StateStarting   = "Starting"
	StateRunning    = "Running"
	StateStopped    = "Stopped"
)

// Config configures the embedded node.
type Config struct {
	Dir        string
	Hostname   string
	ControlURL string // "" = Tailscale's coordination server
	AuthKey    string // optional; TS_AUTHKEY is also honoured by tsnet
	Ephemeral  bool
	InMemory   bool // keep node state in memory (tests)
	Logf       logger.Logf
	UserLogf   logger.Logf
}

// Info is a snapshot of the node's login state for the UI.
type Info struct {
	State       string `json:"state"`
	AuthURL     string `json:"authURL,omitempty"`
	LoginName   string `json:"loginName,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Tailnet     string `json:"tailnet,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	DNSName     string `json:"dnsName,omitempty"`
	IPv4        string `json:"ipv4,omitempty"`
	NodeID      string `json:"nodeID,omitempty"`
	UserID      int64  `json:"userID,omitempty"`
	ControlURL  string `json:"controlURL,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Node is the embedded tailnet node.
type Node struct {
	cfg Config
	srv *tsnet.Server
	lc  *local.Client

	mu       sync.Mutex
	state    string
	authURL  string
	lastErr  string
	onChange func()

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates (but does not start) a node.
func New(cfg Config) *Node {
	srv := &tsnet.Server{
		Dir:        cfg.Dir,
		Hostname:   cfg.Hostname,
		ControlURL: cfg.ControlURL,
		AuthKey:    cfg.AuthKey,
		Ephemeral:  cfg.Ephemeral,
		Logf:       cfg.Logf,
		UserLogf:   cfg.UserLogf,
	}
	if cfg.InMemory {
		srv.Store = new(mem.Store)
	}
	if srv.Logf == nil {
		srv.Logf = logger.Discard
	}
	if srv.UserLogf == nil {
		srv.UserLogf = logger.Discard
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Node{cfg: cfg, srv: srv, state: StateNoState, ctx: ctx, cancel: cancel}
}

// OnChange registers a callback fired whenever login state changes.
func (n *Node) OnChange(fn func()) {
	n.mu.Lock()
	n.onChange = fn
	n.mu.Unlock()
}

func (n *Node) changed() {
	n.mu.Lock()
	fn := n.onChange
	n.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Start brings the node up in the background. Login (if needed) proceeds
// asynchronously; watch Info().AuthURL.
func (n *Node) Start() error {
	if err := n.srv.Start(); err != nil {
		return err
	}
	lc, err := n.srv.LocalClient()
	if err != nil {
		return err
	}
	n.lc = lc
	go n.watch()
	return nil
}

// Up starts the node and waits until it is Running (used with auth keys / tests).
func (n *Node) Up(ctx context.Context) error {
	if _, err := n.srv.Up(ctx); err != nil {
		return err
	}
	if n.lc == nil {
		lc, err := n.srv.LocalClient()
		if err != nil {
			return err
		}
		n.lc = lc
		go n.watch()
	}
	return nil
}

func (n *Node) watch() {
	backoff := time.Second
	for n.ctx.Err() == nil {
		w, err := n.lc.WatchIPNBus(n.ctx, ipn.NotifyInitialState)
		if err != nil {
			n.setErr(err)
			select {
			case <-time.After(backoff):
			case <-n.ctx.Done():
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for {
			note, err := w.Next()
			if err != nil {
				w.Close()
				break
			}
			n.apply(note)
		}
	}
}

func (n *Node) apply(note ipn.Notify) {
	dirty := false
	n.mu.Lock()
	if note.State != nil {
		st := note.State.String()
		if st != n.state {
			n.state = st
			dirty = true
		}
		if *note.State == ipn.Running && n.authURL != "" {
			n.authURL = ""
			dirty = true
		}
	}
	if note.BrowseToURL != nil && *note.BrowseToURL != n.authURL {
		n.authURL = *note.BrowseToURL
		dirty = true
	}
	if note.ErrMessage != nil {
		n.lastErr = *note.ErrMessage
		dirty = true
	}
	if note.LoginFinished != nil {
		n.authURL = ""
		n.lastErr = ""
		dirty = true
	}
	n.mu.Unlock()
	if dirty {
		n.changed()
	}
}

func (n *Node) setErr(err error) {
	n.mu.Lock()
	n.lastErr = err.Error()
	n.mu.Unlock()
	n.changed()
}

// State returns the current backend state string.
func (n *Node) State() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

// Running reports whether the node is connected to the tailnet.
func (n *Node) Running() bool { return n.State() == StateRunning }

// Login starts an interactive login and returns the URL the user must visit.
func (n *Node) Login(ctx context.Context) (string, error) {
	if n.lc == nil {
		return "", errors.New("tailnet not started")
	}
	n.mu.Lock()
	if n.authURL != "" {
		u := n.authURL
		n.mu.Unlock()
		return u, nil
	}
	n.mu.Unlock()
	if err := n.lc.StartLoginInteractive(ctx); err != nil {
		return "", err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		u, st := n.authURL, n.state
		n.mu.Unlock()
		if u != "" {
			return u, nil
		}
		if st == StateRunning {
			return "", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return "", errors.New("timed out waiting for the Tailscale login URL")
}

// Logout signs the node out of the tailnet.
func (n *Node) Logout(ctx context.Context) error {
	if n.lc == nil {
		return errors.New("tailnet not started")
	}
	return n.lc.Logout(ctx)
}

// Status returns the full tailnet status (peers included).
func (n *Node) Status(ctx context.Context) (*ipnstate.Status, error) {
	if n.lc == nil {
		return nil, errors.New("tailnet not started")
	}
	return n.lc.Status(ctx)
}

// WhoIs identifies the tailnet node and user behind remoteAddr ("ip:port").
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
	if n.lc == nil {
		return nil, errors.New("tailnet not started")
	}
	return n.lc.WhoIs(ctx, remoteAddr)
}

// Dial connects to addr over the tailnet.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// Listen listens on the node's tailnet addresses.
func (n *Node) Listen(network, addr string) (net.Listener, error) {
	return n.srv.Listen(network, addr)
}

// IPv4 returns this node's tailnet IPv4 address, if assigned.
func (n *Node) IPv4() netip.Addr {
	ip4, _ := n.srv.TailscaleIPs()
	return ip4
}

// Info returns a UI snapshot of login state.
func (n *Node) Info(ctx context.Context) Info {
	n.mu.Lock()
	inf := Info{State: n.state, AuthURL: n.authURL, Error: n.lastErr, ControlURL: n.cfg.ControlURL}
	n.mu.Unlock()
	if inf.State == "" {
		inf.State = StateNoState
	}
	if n.lc == nil {
		return inf
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		return inf
	}
	if inf.AuthURL == "" && st.BackendState == StateNeedsLogin {
		inf.AuthURL = st.AuthURL
	}
	if st.CurrentTailnet != nil {
		inf.Tailnet = st.CurrentTailnet.Name
	}
	if st.Self != nil {
		inf.Hostname = st.Self.HostName
		inf.DNSName = trimDot(st.Self.DNSName)
		inf.NodeID = string(st.Self.ID)
		inf.UserID = int64(st.Self.UserID)
		if u, ok := st.User[st.Self.UserID]; ok {
			inf.LoginName = u.LoginName
			inf.DisplayName = u.DisplayName
		}
	}
	for _, ip := range st.TailscaleIPs {
		if ip.Is4() {
			inf.IPv4 = ip.String()
		}
	}
	return inf
}

// Close shuts the node down.
func (n *Node) Close() error {
	n.cancel()
	return n.srv.Close()
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

// LinkDescription summarises how traffic to a peer flows.
func LinkDescription(p *ipnstate.PeerStatus) string {
	switch {
	case p == nil:
		return ""
	case p.CurAddr != "":
		if ap, err := netip.ParseAddrPort(p.CurAddr); err == nil && (ap.Addr().IsPrivate() || ap.Addr().IsLinkLocalUnicast()) {
			return "Tailscale · direct (LAN)"
		}
		return "Tailscale · direct"
	case p.PeerRelay != "":
		return "Tailscale · peer relay"
	case p.Relay != "":
		return "Tailscale · relay (" + p.Relay + ")"
	default:
		return "Tailscale"
	}
}
