package core

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/rustdesk"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// ScreenInfo tells paired devices how to view this computer's screen: the
// RustDesk password TailConnect set, if it set one up. A device asks just
// before it connects, so this also starts RustDesk if needed.
func (c *Core) ScreenInfo() agent.ScreenInfo {
	rd := c.store.Get().RustDesk
	if rd.Configured {
		c.ensureRustDesk()
	}
	if rd.Password == "" {
		return agent.ScreenInfo{}
	}
	return agent.ScreenInfo{RustDesk: &agent.RustDeskInfo{Port: agent.RustDeskPort, Password: rd.Password}}
}

// startsOnDemand: on a Mac, RustDesk accepts connections only while its app
// runs (nothing starts it at login unless the user installed its optional
// system service), so TailConnect starts it when a device connects.
func (c *Core) startsOnDemand() bool { return runtime.GOOS == "darwin" && !c.opts.Headless }

// ensureRustDesk starts RustDesk here if it isn't accepting connections and
// can be started, and waits up to 10 seconds for it.
func (c *Core) ensureRustDesk() {
	if !c.startsOnDemand() || rustdesk.Listening() {
		return
	}
	// One start at a time; later callers then find it listening.
	c.screenStartMu.Lock()
	defer c.screenStartMu.Unlock()
	if rustdesk.Listening() || launch.RustDeskRunning() {
		return
	}
	c.logs.Session.Printf("starting RustDesk for a screen-sharing connection")
	if err := launch.StartRustDesk(); err != nil {
		c.logs.Printf("starting RustDesk: %v", err)
		return
	}
	for i := 0; i < 40 && !rustdesk.Listening(); i++ {
		time.Sleep(250 * time.Millisecond)
	}
	c.forgetScreenHealth()
}

// screenHealth caches a look at RustDesk on this computer (the UI asks for
// state often; this dials RustDesk's port and lists processes).
type screenHealth struct {
	at                           time.Time
	listening, running, needsFix bool
}

func (c *Core) rustDeskHealth() screenHealth {
	c.mu.Lock()
	h := c.screenHealth
	c.mu.Unlock()
	if time.Since(h.at) < 15*time.Second {
		return h
	}
	h = screenHealth{at: time.Now(), listening: rustdesk.Listening(), running: launch.RustDeskRunning(), needsFix: rustdesk.NeedsRepair()}
	c.mu.Lock()
	c.screenHealth = h
	c.mu.Unlock()
	return h
}

func (c *Core) forgetScreenHealth() {
	c.mu.Lock()
	c.screenHealth = screenHealth{}
	c.mu.Unlock()
}

// screenView reports screen-sharing setup for the UI.
func (c *Core) screenView() ScreenView {
	st := c.store.Get()
	installed, _ := launch.ScreenViewer(launch.ScreenRustDesk)
	c.mu.Lock()
	running, errText := c.screenSetup.running, c.screenSetup.err
	c.mu.Unlock()
	v := ScreenView{
		RustDeskInstalled: installed,
		Configured:        st.RustDesk.Configured,
		PasswordKnown:     st.RustDesk.Password != "",
		Locked:            st.RustDesk.Locked,
		Version:           st.RustDesk.Version,
		Running:           running,
		Error:             errText,
		StartsOnDemand:    c.startsOnDemand(),
		Wayland:           runtime.GOOS == "linux" && (os.Getenv("XDG_SESSION_TYPE") == "wayland" || os.Getenv("XDG_SESSION_TYPE") == "" && os.Getenv("WAYLAND_DISPLAY") != ""),
	}
	if installed && !c.opts.Headless {
		h := c.rustDeskHealth()
		v.Listening, v.AppRunning, v.NeedsRepair = h.listening, h.running, h.needsFix
	}
	return v
}

// checkScreenSetup tells the user once per start if screen sharing set up
// by TailConnect 0.3.9 needs repairing (RustDesk can't read its settings).
func (c *Core) checkScreenSetup() {
	if c.opts.Headless || !c.store.Get().RustDesk.Configured || !rustdesk.NeedsRepair() {
		return
	}
	c.logs.Printf("screen sharing: RustDesk can't read its settings; setup needs repairing")
	c.notify("Screen sharing needs a quick repair", "RustDesk can't read its settings. Open Settings → Screen sharing and click Repair.", "#/settings/screen")
}

// StartScreenSetup installs (if needed) and configures RustDesk for screen
// sharing through TailConnect, in the background. It asks for
// administrator rights once (RustDesk only accepts settings from an
// administrator). Progress and errors show in the View.
func (c *Core) StartScreenSetup() error {
	if c.opts.Headless {
		return errors.New("screen sharing needs the desktop app")
	}
	c.mu.Lock()
	if c.screenSetup.running {
		c.mu.Unlock()
		return errors.New("screen sharing setup is already running")
	}
	c.screenSetup.running, c.screenSetup.err = true, ""
	c.mu.Unlock()
	c.events.Notify()
	go func() {
		err := c.setupScreen(context.Background())
		c.mu.Lock()
		c.screenSetup.running = false
		if err != nil {
			c.screenSetup.err = err.Error()
		}
		c.screenHealth = screenHealth{}
		c.mu.Unlock()
		c.events.Notify()
	}()
	return nil
}

func (c *Core) setupScreen(ctx context.Context) error {
	st := c.store.Get()
	pw := st.RustDesk.Password
	if pw == "" {
		pw = rustdesk.NewPassword()
	}
	spec := rustdesk.Spec{Password: pw, Lock: st.Settings.RustDeskLock}
	if runtime.GOOS == "darwin" {
		spec.UID = os.Getuid()
	}
	c.logs.Session.Printf("setting up RustDesk for screen sharing (lock=%v)", spec.Lock)
	res, err := rustdesk.Setup(ctx, c.appExe(), spec)
	if err != nil {
		c.logs.Printf("screen sharing setup: %v", err)
		return err
	}
	_ = c.store.Update(func(st *store.State) error {
		st.RustDesk.Configured = true
		st.RustDesk.Locked = spec.Lock
		st.RustDesk.ConfiguredAt = time.Now()
		if res.Version != "" {
			st.RustDesk.Version = res.Version
		}
		if res.PasswordSet {
			st.RustDesk.Password = pw
		} else if st.RustDesk.Password != pw {
			st.RustDesk.Password = "" // RustDesk kept its own password
		}
		return nil
	})
	c.logs.Session.Printf("RustDesk set up (installed=%v version=%s passwordSet=%v)", res.Installed, res.Version, res.PasswordSet)
	if !st.RustDesk.Configured && !st.Settings.StartAtLogin {
		// Devices reach this computer only while TailConnect runs: the
		// first setup also starts it at login (Settings → General).
		if err := c.UpdateSettings(func(s *store.Settings) { s.StartAtLogin = true }); err != nil {
			c.logs.Printf("start at login: %v", err)
		} else {
			c.logs.Session.Printf("start at login turned on for screen sharing")
		}
	}
	if runtime.GOOS == "darwin" {
		// RustDesk asks for Screen Recording and Accessibility itself.
		_ = launch.OpenApp("RustDesk")
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}
