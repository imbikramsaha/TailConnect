package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/forward"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/sshx"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// running is a custom app active on a device.
type running struct {
	AppID      string    `json:"appID"`
	Name       string    `json:"name"`
	LocalPort  int       `json:"localPort"`
	RemotePort int       `json:"remotePort"`
	URL        string    `json:"url"`
	PGID       int       `json:"pgid,omitempty"`
	Status     string    `json:"status"` // starting | running | error
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	// AdHoc marks forwards started without a saved app (detected services).
	AdHoc bool `json:"adHoc,omitempty"`
	fwd   *forward.Forward
}

// remote paths (relative to $HOME) used for custom app scripts and logs.
const remoteDir = ".config/tailconnect"

// SaveApp creates or updates a custom app definition.
func (c *Core) SaveApp(a store.App) (store.App, error) {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return a, errors.New("name is required")
	}
	if !netx.ValidPort(a.Port) {
		return a, errors.New("port must be between 1 and 65535")
	}
	a.Path = strings.TrimPrefix(strings.TrimSpace(a.Path), "/")
	a.Devices = slices.Compact(slices.Sorted(slices.Values(a.Devices)))
	err := c.store.Update(func(st *store.State) error {
		for _, o := range st.Apps {
			overlap := len(o.Devices) == 0 || len(a.Devices) == 0 ||
				slices.ContainsFunc(a.Devices, func(d string) bool { return slices.Contains(o.Devices, d) })
			if o.Port == a.Port && o.ID != a.ID && overlap {
				return fmt.Errorf("port %d is already used by %q", a.Port, o.Name)
			}
		}
		for _, d := range a.Devices {
			if st.FindDevice(d) < 0 {
				return fmt.Errorf("unknown device %q", d)
			}
		}
		if a.ID == "" {
			a.ID = newID("a-")
			st.Apps = append(st.Apps, a)
			return nil
		}
		i := st.FindApp(a.ID)
		if i < 0 {
			return errors.New("unknown app")
		}
		st.Apps[i] = a
		return nil
	})
	c.events.Notify()
	return a, err
}

// DeleteApp removes a custom app definition (stopping it everywhere).
func (c *Core) DeleteApp(id string) error {
	c.mu.Lock()
	var devs []string
	for devID, cs := range c.conns {
		if _, ok := cs.apps[id]; ok {
			devs = append(devs, devID)
		}
	}
	c.mu.Unlock()
	for _, d := range devs {
		_ = c.StopApp(d, id)
	}
	err := c.store.Update(func(st *store.State) error {
		i := st.FindApp(id)
		if i < 0 {
			return errors.New("unknown app")
		}
		st.Apps = append(st.Apps[:i], st.Apps[i+1:]...)
		return nil
	})
	c.events.Notify()
	return err
}

// StartApp runs a custom app on a device: optional launch script, port
// forward to localhost, optional browser/terminal.
func (c *Core) StartApp(ctx context.Context, deviceID, appID string) (*running, error) {
	st := c.store.Get()
	ai := st.FindApp(appID)
	if ai < 0 {
		return nil, errors.New("unknown app")
	}
	app := st.Apps[ai]
	dev, ok := c.device(deviceID)
	if !ok {
		return nil, errors.New("unknown device")
	}
	if !app.AppliesTo(deviceID) {
		return nil, fmt.Errorf("%q isn't set up for %s", app.Name, dev.Name)
	}
	if err := c.ensureConnected(ctx, deviceID); err != nil {
		return nil, err
	}
	cs := c.conn(deviceID)
	if cs == nil {
		return nil, errors.New("not connected")
	}
	c.mu.Lock()
	if r := cs.apps[appID]; r != nil {
		c.mu.Unlock()
		if app.OpenBrowser {
			_ = c.opts.OpenURL(r.URL)
		}
		return r, nil
	}
	r := &running{AppID: app.ID, Name: app.Name, RemotePort: app.Port, Status: "starting", StartedAt: time.Now()}
	cs.apps[appID] = r
	c.mu.Unlock()
	c.events.Notify()

	fail := func(err error) (*running, error) {
		c.mu.Lock()
		delete(cs.apps, appID)
		c.mu.Unlock()
		if r.fwd != nil {
			r.fwd.Close()
		}
		c.logs.Session.Printf("app %q on %q failed: %v", app.Name, dev.Name, err)
		c.events.Notify()
		return nil, err
	}

	// Start the remote service unless something is already listening.
	alreadyUp := c.remotePortOpen(ctx, dev, app.Port)
	script := remoteDir + "/scripts/port-" + strconv.Itoa(app.Port) + ".sh"
	if strings.TrimSpace(app.Script) != "" && !alreadyUp {
		cl, err := c.sshClient(ctx, dev)
		if err != nil {
			return fail(err)
		}
		up := fmt.Sprintf(`mkdir -p "$HOME/%[1]s/scripts" "$HOME/%[1]s/logs" && cat > "$HOME/%[2]s" && chmod 700 "$HOME/%[2]s"`, remoteDir, script)
		if _, err := sshx.Run(ctx, cl, up, strings.NewReader(app.Script+"\n")); err != nil {
			return fail(fmt.Errorf("uploading launch script: %w", err))
		}
		if !app.InTerminal {
			logBase := `$HOME/` + remoteDir + `/logs/port-` + strconv.Itoa(app.Port)
			run := fmt.Sprintf(`bash -c 'set -m; cd "$HOME"; nohup bash -l "$HOME/%s" > "%s.out" 2> "%s.err" < /dev/null & echo $!'`, script, logBase, logBase)
			out, err := sshx.Run(ctx, cl, run, nil)
			if err != nil {
				return fail(fmt.Errorf("starting launch script: %w", err))
			}
			pgid, _ := strconv.Atoi(strings.TrimSpace(out))
			r.PGID = pgid
		}
	}

	f, err := forward.Start(app.Port, func(ctx context.Context) (net.Conn, error) {
		d, ok := c.device(deviceID)
		if !ok {
			return nil, errors.New("device removed")
		}
		return c.dialPort(ctx, d, app.Port)
	}, c.logs.Printf)
	if err != nil {
		return fail(fmt.Errorf("local port: %w", err))
	}
	c.mu.Lock()
	r.fwd = f
	r.LocalPort = f.Port
	r.URL = "http://localhost:" + strconv.Itoa(f.Port) + "/" + app.Path
	c.mu.Unlock()

	if app.InTerminal && !c.opts.Headless {
		cmd := `bash -l "$HOME/` + script + `"; exec "${SHELL:-bash}" -l`
		if strings.TrimSpace(app.Script) == "" {
			cmd = ""
		}
		if err := launch.Terminal(c.store.Get().Settings.Terminal, launch.SSHCommand(dev.Alias, cmd)); err != nil {
			c.logs.Printf("terminal: %v", err)
		}
	}

	c.setRunning(cs, r, "running", "")
	c.logs.Session.Printf("app %q on %q: remote :%d -> localhost:%d", app.Name, dev.Name, app.Port, f.Port)

	if app.OpenBrowser && !c.opts.Headless {
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if c.conn(deviceID) != cs {
					return
				}
				if c.remotePortOpen(c.ctx, dev, app.Port) {
					_ = c.opts.OpenURL(r.URL)
					return
				}
				time.Sleep(time.Second)
			}
			c.setRunning(cs, r, "running", "service did not start listening within 30s (see ~/"+remoteDir+"/logs on the device)")
		}()
	}
	return r, nil
}

func (c *Core) setRunning(cs *connState, r *running, status, msg string) {
	c.mu.Lock()
	r.Status, r.Error = status, msg
	c.mu.Unlock()
	c.events.Notify()
}

func (c *Core) remotePortOpen(ctx context.Context, dev store.Device, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := c.dialPort(ctx, dev, port)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// StopApp stops a running custom app on a device.
func (c *Core) StopApp(deviceID, appID string) error {
	cs := c.conn(deviceID)
	if cs == nil {
		return errors.New("not connected")
	}
	c.mu.Lock()
	r := cs.apps[appID]
	delete(cs.apps, appID)
	c.mu.Unlock()
	if r == nil {
		return errors.New("app is not running")
	}
	dev, _ := c.device(deviceID)
	c.stopRunning(dev, cs, r)
	c.events.Notify()
	return nil
}

func (c *Core) stopRunning(dev store.Device, cs *connState, r *running) {
	if r.fwd != nil {
		r.fwd.Close()
	}
	if r.PGID > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.mu.Lock()
		cl := cs.ssh
		c.mu.Unlock()
		own := false
		if cl == nil || !sshClientOK(cl) {
			var err error
			if cl, err = sshx.Connect(ctx, c.sshParams(dev, "")); err != nil {
				c.logs.Printf("stop %q: %v", r.Name, err)
				cl = nil
			}
			own = true
		}
		if cl != nil {
			pg := strconv.Itoa(r.PGID)
			_, _ = sshx.Run(ctx, cl, "kill -TERM -- -"+pg+" 2>/dev/null || kill -TERM "+pg+" 2>/dev/null; true", nil)
			if own {
				cl.Close()
			}
		}
	}
	c.logs.Session.Printf("stopped app %q on %q", r.Name, dev.Name)
}
