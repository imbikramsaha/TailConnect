// Package core is TailConnect's engine: it owns the embedded tailnet node,
// discovers and pairs devices, manages connections, port forwards, custom
// apps, launchers and the resource monitor. The tray, the local web UI and
// the CLI are thin clients of Core.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/autostart"
	"github.com/imbikramsaha/tailconnect/internal/hostname"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/logx"
	"github.com/imbikramsaha/tailconnect/internal/sshconf"
	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
)

// Options configure a Core.
type Options struct {
	Version string
	Logs    *logx.Logs
	Store   *store.Store
	Tailnet tailnet.Config
	// Exe is the tailconnect binary used in ssh ProxyCommand lines.
	Exe string
	// SSHDir holds the key pair, known_hosts and generated ssh config.
	SSHDir string
	// AuthorizedKeys is where trusted peers' keys are installed.
	AuthorizedKeys string
	// UserSSHConfig, if set, gets an Include of the generated config.
	UserSSHConfig string
	// OpenURL opens URLs for the user (nil = system browser).
	OpenURL func(string) error
	// DiscoveryInterval is how often tailnet status is refreshed.
	DiscoveryInterval time.Duration
	// ProbeTailConnect / ProbeOther are how often tailnet nodes are asked
	// whether they run TailConnect (known ones / others).
	ProbeTailConnect, ProbeOther time.Duration
	// Headless disables desktop integrations (autostart, launching apps).
	Headless bool
	// InstallWarning, if set, is shown in the UI (e.g. "move to Applications").
	InstallWarning string
}

// Core is the running engine.
type Core struct {
	opts     Options
	store    *store.Store
	logs     *logx.Logs
	node     *tailnet.Node
	agentCli *agent.Client
	agentSrv *agent.Server
	id       *sshkeys.Identity
	known    *sshkeys.KnownHosts
	events   *Broker
	selfName string
	osUser   string
	home     string

	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{}

	mu         sync.Mutex
	peers      map[string]*peerInfo  // by StableNodeID
	conns      map[string]*connState // by device ID
	incoming   map[string]*incoming  // pairing requests to this device, by request ID
	outgoing   map[string]*outgoing  // our pairing requests, by node ID
	selfUserID int64
	selfNodeID string
}

// New prepares a Core; call Start to bring it up.
func New(opts Options) (*Core, error) {
	if opts.Logs == nil {
		opts.Logs = logx.Discard()
	}
	if opts.DiscoveryInterval == 0 {
		opts.DiscoveryInterval = 5 * time.Second
	}
	if opts.ProbeTailConnect == 0 {
		opts.ProbeTailConnect = defaultProbeTailConnect
	}
	if opts.ProbeOther == 0 {
		opts.ProbeOther = defaultProbeOther
	}
	if opts.OpenURL == nil {
		opts.OpenURL = launch.OpenURL
	}
	name := opts.Store.Get().Settings.Hostname
	if name == "" {
		name = hostname.Friendly()
	}
	if name == "" {
		name = "device"
	}
	if err := os.MkdirAll(opts.SSHDir, 0o700); err != nil {
		return nil, err
	}
	id, err := sshkeys.LoadOrCreate(opts.SSHDir, "tailconnect@"+sanitizeName(name))
	if err != nil {
		return nil, fmt.Errorf("ssh key: %w", err)
	}
	c := &Core{
		opts:     opts,
		store:    opts.Store,
		logs:     opts.Logs,
		id:       id,
		known:    sshkeys.NewKnownHosts(filepath.Join(opts.SSHDir, "known_hosts")),
		events:   NewBroker(),
		selfName: name,
		kick:     make(chan struct{}, 1),
		peers:    map[string]*peerInfo{},
		conns:    map[string]*connState{},
		incoming: map[string]*incoming{},
		outgoing: map[string]*outgoing{},
	}
	if u, err := user.Current(); err == nil {
		c.osUser = u.Username
		if i := strings.LastIndex(c.osUser, `\`); i >= 0 { // DOMAIN\user on Windows
			c.osUser = c.osUser[i+1:]
		}
	}
	c.home, _ = os.UserHomeDir()
	c.node = tailnet.New(opts.Tailnet)
	c.agentCli = agent.NewClient(c.node.Dial)
	return c, nil
}

func sanitizeName(s string) string {
	return strings.TrimPrefix(sshconf.Alias(s), "tc-")
}

// Start brings the tailnet node and agent API up and starts discovery.
func (c *Core) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.node.OnChange(func() {
		c.Kick()
		c.events.Notify()
	})
	if err := c.node.Start(); err != nil {
		return fmt.Errorf("starting tailnet node: %w", err)
	}
	ln, err := c.node.Listen("tcp", fmt.Sprintf(":%d", agent.Port))
	if err != nil {
		return fmt.Errorf("agent listener: %w", err)
	}
	c.agentSrv = &agent.Server{ID: c, Host: c, Logf: c.logs.Printf}
	go func() {
		if err := c.agentSrv.Serve(ln); err != nil {
			c.logs.Printf("agent server: %v", err)
		}
	}()
	if err := c.writeSSHConfig(); err != nil {
		c.logs.Printf("ssh config: %v", err)
	}
	if st := c.store.Get().Settings; st.StartAtLogin && !c.opts.Headless {
		if err := autostart.Set(true, c.opts.Exe); err != nil { // refresh path after updates
			c.logs.Printf("autostart: %v", err)
		}
	}
	go c.discoveryLoop()
	return nil
}

// Up waits for the node to be Running (auth key / tests).
func (c *Core) Up(ctx context.Context) error { return c.node.Up(ctx) }

// Close shuts everything down.
func (c *Core) Close() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.conns))
	for id := range c.conns {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.Disconnect(id)
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.agentSrv != nil {
		c.agentSrv.Close()
	}
	c.node.Close()
}

// Events returns the change broker (UI/tray subscribe to it).
func (c *Core) Events() *Broker { return c.events }

// Kick triggers an immediate discovery refresh.
func (c *Core) Kick() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Name is this device's display name.
func (c *Core) Name() string { return c.selfName }

// Identity returns this device's SSH identity.
func (c *Core) Identity() *sshkeys.Identity { return c.id }

// Node exposes the tailnet node (CLI/tests).
func (c *Core) Node() *tailnet.Node { return c.node }

// Login starts Tailscale sign-in and opens the browser (unless headless).
func (c *Core) Login(ctx context.Context) (string, error) {
	u, err := c.node.Login(ctx)
	if err != nil {
		return "", err
	}
	if u != "" && !c.opts.Headless {
		if err := c.opts.OpenURL(u); err != nil {
			c.logs.Printf("open login url: %v", err)
		}
	}
	return u, nil
}

// Logout signs out of the tailnet and disconnects all devices.
func (c *Core) Logout(ctx context.Context) error {
	c.mu.Lock()
	ids := make([]string, 0, len(c.conns))
	for id := range c.conns {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.Disconnect(id)
	}
	err := c.node.Logout(ctx)
	c.events.Notify()
	return err
}

// UpdateSettings applies and persists settings changes.
func (c *Core) UpdateSettings(fn func(*store.Settings)) error {
	before := c.store.Get().Settings
	if err := c.store.Update(func(st *store.State) error {
		fn(&st.Settings)
		if st.Settings.LocalSSHPort <= 0 || st.Settings.LocalSSHPort > 65535 {
			st.Settings.LocalSSHPort = 22
		}
		return nil
	}); err != nil {
		return err
	}
	after := c.store.Get().Settings
	var errs []error
	if before.StartAtLogin != after.StartAtLogin && !c.opts.Headless {
		if err := autostart.Set(after.StartAtLogin, c.opts.Exe); err != nil {
			errs = append(errs, fmt.Errorf("start at login: %w", err))
		}
	}
	if before.ManageSSHConfig != after.ManageSSHConfig {
		if err := c.writeSSHConfig(); err != nil {
			errs = append(errs, err)
		}
		if !after.ManageSSHConfig && c.opts.UserSSHConfig != "" {
			if err := sshconf.RemoveInclude(c.opts.UserSSHConfig, c.managedConfigPath()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if !after.AutoTrustOwnDevices && before.AutoTrustOwnDevices {
		c.logs.Session.Printf("auto-trust of own devices disabled")
	}
	c.Kick()
	c.events.Notify()
	return errors.Join(errs...)
}

// RestartRequired reports settings that only apply after restart.
func (c *Core) RestartRequired() bool {
	st := c.store.Get().Settings
	return st.ControlURL != c.opts.Tailnet.ControlURL
}

func newID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// sshClientOK reports whether an ssh client is still alive.
func sshClientOK(cl *ssh.Client) bool {
	if cl == nil {
		return false
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := cl.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(5 * time.Second):
		return false
	}
}

func osName() string { return runtime.GOOS }
