package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/sshconf"
	"github.com/imbikramsaha/tailconnect/internal/sshx"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/sysinfo"
)

// AddSSHRequest adds any SSH host (like NVIDIA Sync's "add device").
type AddSSHRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	// Via is "direct", "tailnet" or "" (auto-detect).
	Via string `json:"via"`
}

// AddSSHDevice connects once with the password, installs TailConnect's key,
// verifies key login and saves the device. The password is never stored.
func (c *Core) AddSSHDevice(ctx context.Context, req AddSSHRequest) (store.Device, error) {
	req.Host = strings.TrimSpace(req.Host)
	req.User = strings.TrimSpace(req.User)
	req.Name = strings.TrimSpace(req.Name)
	if req.Host == "" || req.User == "" {
		return store.Device{}, errors.New("host and username are required")
	}
	if strings.ContainsAny(req.Host+req.User, " \t\n'\"`$;|&") {
		return store.Device{}, errors.New("host or username contains invalid characters")
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Name == "" {
		req.Name = req.Host
	}
	if req.Via == "" {
		req.Via = store.ViaDirect
		if c.isTailnetHost(req.Host) {
			req.Via = store.ViaTailnet
		}
	}
	st := c.store.Get()
	dev := store.Device{
		ID: newID("d-"), Kind: store.KindSSH, Name: req.Name, Alias: uniqueAlias(&st, req.Name, ""),
		Host: req.Host, Port: req.Port, User: req.User, Via: req.Via, AddedAt: time.Now(),
	}
	// Temporarily register so DialDevice can resolve it.
	if err := c.store.Update(func(st *store.State) error { st.Devices = append(st.Devices, dev); return nil }); err != nil {
		return dev, err
	}
	rollback := func(err error) (store.Device, error) {
		_ = c.store.Update(func(st *store.State) error {
			st.Devices = slices.DeleteFunc(st.Devices, func(d store.Device) bool { return d.ID == dev.ID })
			return nil
		})
		_ = c.known.Forget(dev.Alias)
		return store.Device{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cl, err := sshx.Connect(ctx, c.sshParams(dev, req.Password))
	if err != nil {
		return rollback(err)
	}
	err = sshx.InstallKey(ctx, cl, c.id.PublicLine, "tailconnect@"+sanitizeName(c.selfName))
	home := sshx.HomeDir(ctx, cl)
	cl.Close()
	if err != nil {
		return rollback(err)
	}
	// Verify key-only login works.
	cl, err = sshx.Connect(ctx, c.sshParams(dev, ""))
	if err != nil {
		return rollback(fmt.Errorf("key login failed after setup: %w", err))
	}
	cl.Close()
	dev.Home = home
	if err := c.store.Update(func(st *store.State) error {
		if i := st.FindDevice(dev.ID); i >= 0 {
			st.Devices[i].Home = home
		}
		return nil
	}); err != nil {
		return dev, err
	}
	c.logs.Session.Printf("added SSH device %q (%s@%s:%d via %s)", dev.Name, dev.User, dev.Host, dev.Port, dev.Via)
	err = c.writeSSHConfig()
	c.events.Notify()
	return dev, err
}

// RenameDevice renames a device and updates its SSH alias.
func (c *Core) RenameDevice(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is required")
	}
	var oldAlias string
	err := c.store.Update(func(st *store.State) error {
		i := st.FindDevice(id)
		if i < 0 {
			return errors.New("unknown device")
		}
		oldAlias = st.Devices[i].Alias
		st.Devices[i].Name = name
		st.Devices[i].Alias = uniqueAlias(st, name, id)
		return nil
	})
	if err != nil {
		return err
	}
	_ = c.known.Forget(oldAlias)
	c.events.Notify()
	return c.writeSSHConfig()
}

// RemoveDevice forgets a device. For TailConnect peers it also unpairs in
// both directions and stops auto-pairing with it.
func (c *Core) RemoveDevice(ctx context.Context, id string) error {
	dev, ok := c.device(id)
	if !ok {
		return errors.New("unknown device")
	}
	c.Disconnect(id)
	if dev.Kind == store.KindPeer {
		if p, ok := c.peer(dev.NodeID); ok && p.IP != "" {
			uctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := c.agentCli.Unpair(uctx, p.IP); err != nil {
				c.logs.Printf("unpair %s: %v", dev.Name, err)
			}
			cancel()
		}
		if c.IsTrusted(dev.NodeID) {
			_ = c.untrust(dev.NodeID)
		}
	}
	err := c.store.Update(func(st *store.State) error {
		st.Devices = slices.DeleteFunc(st.Devices, func(d store.Device) bool { return d.ID == id })
		if dev.NodeID != "" && !slices.Contains(st.Ignored, dev.NodeID) {
			st.Ignored = append(st.Ignored, dev.NodeID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.outgoing, dev.NodeID)
	c.mu.Unlock()
	_ = c.known.Forget(dev.Alias)
	c.logs.Session.Printf("removed device %q", dev.Name)
	c.events.Notify()
	return c.writeSSHConfig()
}

func (c *Core) managedConfigPath() string { return filepath.Join(c.opts.SSHDir, "config") }

// writeSSHConfig regenerates the managed ssh config (and the Include).
func (c *Core) writeSSHConfig() error {
	st := c.store.Get()
	var entries []sshconf.Entry
	for _, d := range st.Devices {
		if d.Imported {
			continue
		}
		e := sshconf.Entry{Alias: d.Alias, User: d.User, Port: d.Port, DeviceID: d.ID}
		switch {
		case d.Kind == store.KindPeer:
			e.Proxy = true
		case d.Via == store.ViaTailnet:
			e.Proxy = true
			e.HostName = d.Host
		default:
			e.HostName = d.Host
		}
		entries = append(entries, e)
	}
	content := sshconf.Render(entries, sshconf.Options{
		Exe: c.opts.Exe, KeyPath: c.id.PrivPath, KnownHosts: c.known.Path(),
	})
	if err := sshconf.Write(c.managedConfigPath(), content); err != nil {
		return err
	}
	if st.Settings.ManageSSHConfig && c.opts.UserSSHConfig != "" {
		return sshconf.EnsureInclude(c.opts.UserSSHConfig, c.managedConfigPath())
	}
	return nil
}

// Launch opens a built-in app (terminal, editor) connected to a device.
func (c *Core) Launch(ctx context.Context, id, appID string) error {
	dev, ok := c.device(id)
	if !ok {
		return errors.New("unknown device (pair it first)")
	}
	if c.opts.Headless {
		return errors.New("launching apps is not available in headless mode")
	}
	if _, ok := launch.FindBuiltin(appID); !ok {
		return fmt.Errorf("unknown app %q", appID)
	}
	if dev.Kind == store.KindPeer {
		if p, ok := c.peer(dev.NodeID); !ok || !p.Online {
			return fmt.Errorf("%s is offline", dev.Name)
		} else if p.Hello != nil && !p.Hello.SSH && p.Hello.BuiltinSSH == 0 {
			if p.Hello.Access != "" && p.Hello.Access != store.AccessFull {
				return fmt.Errorf("%s only gives this computer %q access; terminals and editors need full access (change it in TailConnect on %s)", dev.Name, p.Hello.Access, dev.Name)
			}
			return ErrNoSSH(dev)
		}
	}
	if err := c.writeSSHConfig(); err != nil {
		c.logs.Printf("ssh config: %v", err)
	}
	if c.conn(id) == nil {
		_ = c.Connect(id)
	}
	dev, _ = c.device(id)
	if err := launch.App(appID, launch.Target{Alias: dev.Alias, Home: dev.Home}, c.store.Get().Settings.Terminal); err != nil {
		return err
	}
	c.logs.Session.Printf("launched %s on %q", appID, dev.Name)
	return nil
}

// Stats returns a resource snapshot for a device.
func (c *Core) Stats(ctx context.Context, id string) (*sysinfo.Stats, error) {
	if id == "self" {
		return sysinfo.Collect(ctx, 400*time.Millisecond)
	}
	dev, ok := c.device(id)
	if !ok {
		return nil, errors.New("unknown device")
	}
	if dev.Kind == store.KindPeer {
		p, ok := c.peer(dev.NodeID)
		if !ok || !p.Online {
			return nil, errors.New("device is offline")
		}
		return c.agentCli.Stats(ctx, p.IP)
	}
	cl, err := c.sshClient(ctx, dev)
	if err != nil {
		if err := c.ensureConnected(ctx, id); err != nil {
			return nil, err
		}
		if cl, err = c.sshClient(ctx, dev); err != nil {
			return nil, err
		}
	}
	out, err := sshx.Run(ctx, cl, "sh -s", strings.NewReader(sysinfo.RemoteScript))
	if err != nil && out == "" {
		return nil, err
	}
	s := sysinfo.ParseRemote(out)
	return s, nil
}
