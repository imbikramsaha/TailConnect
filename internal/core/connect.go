package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/sshx"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// Connection states.
const (
	StateDisconnected = "disconnected"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateReconnecting = "reconnecting"
	StateError        = "error"
)

type connState struct {
	State  string
	Err    string
	Since  time.Time
	ssh    *ssh.Client
	cancel context.CancelFunc
	apps   map[string]*running // by app ID
}

// device returns the saved device with id.
func (c *Core) device(id string) (store.Device, bool) {
	st := c.store.Get()
	i := st.FindDevice(id)
	if i < 0 {
		return store.Device{}, false
	}
	return st.Devices[i], true
}

// Connect starts connecting to a device (asynchronously). Discovered,
// unpaired TailConnect devices are paired first.
func (c *Core) Connect(id string) error {
	dev, ok := c.device(id)
	if !ok {
		if _, isPeer := nodeFromDeviceID(id); isPeer {
			return c.PairDevice(id)
		}
		return errors.New("unknown device")
	}
	c.mu.Lock()
	if cs := c.conns[id]; cs != nil && cs.State != StateError {
		c.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(c.ctx)
	cs := &connState{State: StateConnecting, Since: time.Now(), cancel: cancel, apps: map[string]*running{}}
	c.conns[id] = cs
	c.mu.Unlock()
	c.events.Notify()
	go c.runConn(ctx, dev, cs)
	return nil
}

// ensureConnected connects if needed and waits until connected.
func (c *Core) ensureConnected(ctx context.Context, id string) error {
	if err := c.Connect(id); err != nil {
		return err
	}
	ch, unsub := c.events.Subscribe()
	defer unsub()
	timeout := time.After(30 * time.Second)
	for {
		c.mu.Lock()
		cs := c.conns[id]
		state, msg := "", ""
		if cs != nil {
			state, msg = cs.State, cs.Err
		}
		c.mu.Unlock()
		switch state {
		case StateConnected:
			return nil
		case StateError, "":
			if msg == "" {
				msg = "connection failed"
			}
			return errors.New(msg)
		}
		select {
		case <-ch:
		case <-time.After(500 * time.Millisecond):
		case <-timeout:
			return errors.New("timed out connecting")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Core) setConn(id string, cs *connState, state, msg string) {
	c.mu.Lock()
	if c.conns[id] != cs { // replaced or disconnected
		c.mu.Unlock()
		return
	}
	if cs.State != state {
		cs.Since = time.Now()
	}
	cs.State, cs.Err = state, msg
	c.mu.Unlock()
	c.events.Notify()
}

func (c *Core) runConn(ctx context.Context, dev store.Device, cs *connState) {
	backoff := 2 * time.Second
	everConnected := false
	for ctx.Err() == nil {
		err := c.establish(ctx, dev, cs)
		if err == nil {
			if !everConnected {
				c.logs.Session.Printf("connected to %q", dev.Name)
			} else {
				c.logs.Session.Printf("reconnected to %q", dev.Name)
			}
			everConnected = true
			backoff = 2 * time.Second
			c.setConn(dev.ID, cs, StateConnected, "")
			err = c.supervise(ctx, dev, cs)
			if ctx.Err() != nil {
				return
			}
		}
		if !everConnected {
			c.logs.Session.Printf("connecting to %q failed: %v", dev.Name, err)
			c.setConn(dev.ID, cs, StateError, err.Error())
			return
		}
		c.setConn(dev.ID, cs, StateReconnecting, err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		if d, ok := c.device(dev.ID); ok {
			dev = d
		}
	}
}

// establish verifies the device is reachable (and trusts us).
func (c *Core) establish(ctx context.Context, dev store.Device, cs *connState) error {
	switch dev.Kind {
	case store.KindPeer:
		p, ok := c.peer(dev.NodeID)
		if !ok || !p.Online || p.IP == "" {
			return errors.New("device is offline")
		}
		hctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		h, err := c.agentCli.Hello(hctx, p.IP)
		cancel()
		if err != nil {
			return fmt.Errorf("device not responding: %w", err)
		}
		if !h.Trusted {
			return errors.New("this device no longer trusts you; remove it and pair again")
		}
		c.applyHello(dev, h)
		return nil
	case store.KindSSH:
		cl, err := sshx.Connect(ctx, c.sshParams(dev, ""))
		if err != nil {
			return err
		}
		c.mu.Lock()
		old := cs.ssh
		cs.ssh = cl
		c.mu.Unlock()
		if old != nil {
			old.Close()
		}
		if dev.Home == "" {
			hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			home := sshx.HomeDir(hctx, cl)
			cancel()
			if home != "" {
				_ = c.store.Update(func(st *store.State) error {
					if i := st.FindDevice(dev.ID); i >= 0 {
						st.Devices[i].Home = home
					}
					return nil
				})
			}
		}
		return nil
	}
	return fmt.Errorf("unknown device kind %q", dev.Kind)
}

// supervise blocks until the connection fails.
func (c *Core) supervise(ctx context.Context, dev store.Device, cs *connState) error {
	if dev.Kind == store.KindSSH {
		c.mu.Lock()
		cl := cs.ssh
		c.mu.Unlock()
		werr := make(chan error, 1)
		go func() { werr <- cl.Wait() }()
		kctx, cancel := context.WithCancel(ctx)
		defer cancel()
		kerr := make(chan error, 1)
		go func() { kerr <- sshx.Keepalive(kctx, cl, 15*time.Second) }()
		var err error
		select {
		case <-ctx.Done():
			return nil
		case err = <-werr:
		case err = <-kerr:
		}
		cl.Close()
		c.mu.Lock()
		if cs.ssh == cl {
			cs.ssh = nil
		}
		c.mu.Unlock()
		if err == nil {
			err = errors.New("connection closed")
		}
		return err
	}
	fails := 0
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		p, ok := c.peer(dev.NodeID)
		var err error
		if !ok || !p.Online {
			err = errors.New("device went offline")
		} else {
			hctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			var trusted bool
			if h, herr := c.agentCli.Hello(hctx, p.IP); herr != nil {
				err = herr
			} else {
				trusted = h.Trusted
			}
			cancel()
			if err == nil && !trusted {
				return errors.New("this device no longer trusts you")
			}
		}
		if err == nil {
			fails = 0
			continue
		}
		if fails++; fails >= 2 {
			return err
		}
	}
}

// Disconnect stops apps and tunnels for a device.
func (c *Core) Disconnect(id string) {
	c.mu.Lock()
	cs := c.conns[id]
	delete(c.conns, id)
	c.mu.Unlock()
	if cs == nil {
		return
	}
	dev, _ := c.device(id)
	for _, r := range cs.apps {
		c.stopRunning(dev, cs, r)
	}
	cs.cancel()
	c.mu.Lock()
	cl := cs.ssh
	cs.ssh = nil
	c.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
	if dev.Name != "" {
		c.logs.Session.Printf("disconnected from %q", dev.Name)
	}
	c.events.Notify()
}

// connState returns the live connection for a device (nil if none).
func (c *Core) conn(id string) *connState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conns[id]
}

// sshParams builds SSH connection parameters for a device.
func (c *Core) sshParams(dev store.Device, password string) sshx.Params {
	return sshx.Params{
		Alias:      dev.Alias,
		User:       dev.User,
		Signer:     c.id.Signer,
		Password:   password,
		KnownHosts: c.known,
		Dial:       func(ctx context.Context) (net.Conn, error) { return c.sshDial(ctx, dev) },
	}
}

// sshDial opens a raw TCP stream to the device's SSH server.
func (c *Core) sshDial(ctx context.Context, dev store.Device) (net.Conn, error) {
	port := dev.Port
	if port == 0 {
		port = 22
	}
	return c.DialDevice(ctx, dev.ID, port)
}

// DialDevice opens a TCP stream to port on a device: through the agent
// tunnel for TailConnect peers, over the tailnet or directly for SSH hosts.
// Used by `tailconnect proxy` (ssh ProxyCommand) and port forwards.
func (c *Core) DialDevice(ctx context.Context, id string, port int) (net.Conn, error) {
	dev, ok := c.device(id)
	if !ok {
		return nil, errors.New("unknown device")
	}
	switch {
	case dev.Kind == store.KindPeer:
		p, ok := c.peer(dev.NodeID)
		if !ok || p.IP == "" {
			return nil, errors.New("device is offline")
		}
		if port == agent.SSHPort {
			// The built-in SSH server listens on the tailnet itself.
			return c.node.Dial(ctx, "tcp", net.JoinHostPort(p.IP, strconv.Itoa(port)))
		}
		return c.agentCli.Tunnel(ctx, p.IP, port)
	case dev.Via == store.ViaTailnet:
		if !c.node.Running() {
			return nil, errors.New("not connected to Tailscale")
		}
		return c.node.Dial(ctx, "tcp", net.JoinHostPort(dev.Host, strconv.Itoa(port)))
	default:
		d := net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(dev.Host, strconv.Itoa(port)))
	}
}

// dialPort opens a stream to a remote loopback port for forwards: agent
// tunnel for peers, SSH direct-tcpip for SSH hosts.
func (c *Core) dialPort(ctx context.Context, dev store.Device, port int) (net.Conn, error) {
	if dev.Kind == store.KindPeer {
		return c.DialDevice(ctx, dev.ID, port)
	}
	cl, err := c.sshClient(ctx, dev)
	if err != nil {
		return nil, err
	}
	return cl.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// sshClient returns a live SSH client for a device, creating one for peers
// on demand (used for custom app scripts and stats of SSH hosts).
func (c *Core) sshClient(ctx context.Context, dev store.Device) (*ssh.Client, error) {
	cs := c.conn(dev.ID)
	c.mu.Lock()
	var cl *ssh.Client
	if cs != nil {
		cl = cs.ssh
	}
	c.mu.Unlock()
	if cl != nil && sshClientOK(cl) {
		return cl, nil
	}
	if dev.Kind == store.KindSSH {
		return nil, errors.New("not connected")
	}
	if p, ok := c.peer(dev.NodeID); !ok || p.Hello == nil || !p.Hello.SSH {
		return nil, ErrNoSSH(dev)
	}
	cl, err := sshx.Connect(ctx, c.sshParams(dev, ""))
	if err != nil {
		return nil, err
	}
	if cs != nil {
		c.mu.Lock()
		old := cs.ssh
		cs.ssh = cl
		c.mu.Unlock()
		if old != nil {
			old.Close()
		}
	}
	return cl, nil
}

// ErrNoSSH explains how to enable an SSH server on a peer.
func ErrNoSSH(dev store.Device) error {
	hint := "install and start an SSH server (e.g. sudo apt install openssh-server)"
	switch dev.OS {
	case "darwin":
		hint = "turn on System Settings → General → Sharing → Remote Login"
	case "windows":
		hint = "enable Settings → System → Optional features → OpenSSH Server, then start the sshd service"
	}
	return fmt.Errorf("%s has no SSH server available: turn on \"Built-in SSH\" in TailConnect's settings on that device, or %s", dev.Name, hint)
}

// applyHello updates a saved peer from its latest hello: login details,
// which SSH server to use, and Wake-on-LAN info.
func (c *Core) applyHello(dev store.Device, h *agent.Hello) {
	port := h.SSHPort
	if h.BuiltinSSH > 0 {
		port = h.BuiltinSSH
	}
	if port == 0 {
		port = 22
	}
	macs, lans := dev.MACs, dev.LANs
	if len(h.MACs) > 0 {
		macs, lans = h.MACs, h.LANs
	}
	if h.User == dev.User && h.Home == dev.Home && h.OS == dev.OS && port == dev.Port &&
		slices.Equal(macs, dev.MACs) && slices.Equal(lans, dev.LANs) {
		return
	}
	if port != dev.Port && dev.Port != 0 {
		_ = c.known.Forget(dev.Alias) // different SSH server, different host key
	}
	_ = c.store.Update(func(st *store.State) error {
		if i := st.FindDevice(dev.ID); i >= 0 {
			d := &st.Devices[i]
			d.User, d.Home, d.OS, d.Port, d.MACs, d.LANs = h.User, h.Home, h.OS, port, macs, lans
		}
		return nil
	})
	if err := c.writeSSHConfig(); err != nil {
		c.logs.Printf("ssh config: %v", err)
	}
}
