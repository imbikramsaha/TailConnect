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
// RustDesk password TailConnect set, if it set one up.
func (c *Core) ScreenInfo() agent.ScreenInfo {
	rd := c.store.Get().RustDesk
	if rd.Password == "" {
		return agent.ScreenInfo{}
	}
	return agent.ScreenInfo{RustDesk: &agent.RustDeskInfo{Port: agent.RustDeskPort, Password: rd.Password}}
}

// screenView reports screen-sharing setup for the UI.
func (c *Core) screenView() ScreenView {
	st := c.store.Get()
	installed, _ := launch.ScreenViewer(launch.ScreenRustDesk)
	c.mu.Lock()
	running, errText := c.screenSetup.running, c.screenSetup.err
	c.mu.Unlock()
	return ScreenView{
		RustDeskInstalled: installed,
		Configured:        st.RustDesk.Configured,
		PasswordKnown:     st.RustDesk.Password != "",
		Locked:            st.RustDesk.Locked,
		Version:           st.RustDesk.Version,
		Running:           running,
		Error:             errText,
	}
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
	if runtime.GOOS == "darwin" {
		// RustDesk asks for Screen Recording and Accessibility itself.
		_ = launch.OpenApp("RustDesk")
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}
