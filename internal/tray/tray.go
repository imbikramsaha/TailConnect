// Package tray is TailConnect's system-tray menu: device list with live
// status, connect/disconnect, one-click app launchers, custom apps and
// pairing approvals — the quick-access surface (like NVIDIA Sync's tray).
package tray

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/icon"
	"github.com/imbikramsaha/tailconnect/internal/logx"
)

// Tray drives the tray icon and menu.
type Tray struct {
	Core *core.Core
	Logs *logx.Logs
	// ShowUI opens the main window at a hash route.
	ShowUI func(route string)
	// OnQuit is called when the user picks Quit (after the tray exits).
	OnQuit func()

	mu     sync.Mutex
	sig    string
	gen    context.CancelFunc
	active *bool
}

// Run blocks running the tray; it must be called from the main goroutine.
func (t *Tray) Run() {
	systray.Run(t.onReady, func() {
		if t.OnQuit != nil {
			t.OnQuit()
		}
	})
}

// Quit removes the tray icon and makes Run return.
func Quit() { systray.Quit() }

func setIcon(active bool) {
	switch runtime.GOOS {
	case "windows":
		systray.SetIcon(icon.ICO(active))
	case "darwin":
		systray.SetTemplateIcon(icon.Template(active), icon.PNG(active))
	default:
		systray.SetIcon(icon.PNG(active))
	}
}

func (t *Tray) onReady() {
	setIcon(false)
	systray.SetTitle("")
	systray.SetTooltip("TailConnect")
	if runtime.GOOS != "darwin" {
		// On macOS a tap handler would replace the menu on left click;
		// Mac users expect the menu, so keep the default there.
		systray.SetOnTapped(func() { t.ShowUI("") })
	}
	ch, _ := t.Core.Events().Subscribe()
	t.rebuild()
	go func() {
		for range ch {
			time.Sleep(300 * time.Millisecond) // debounce bursts
			select {
			case <-ch:
			default:
			}
			t.rebuild()
		}
	}()
	// Periodic refresh for relative state (link changes etc.).
	go func() {
		for range time.Tick(20 * time.Second) {
			t.rebuild()
		}
	}()
}

type entry struct {
	title    string
	disabled bool
	checked  bool
	sep      bool
	click    func()
	children []entry
}

func (t *Tray) menu(v core.View) []entry {
	var m []entry
	running := v.Tailnet.State == "Running"
	if running {
		who := v.Tailnet.LoginName
		if who == "" {
			who = "Tailscale"
		}
		m = append(m, entry{title: "Connected · " + who, disabled: true})
	} else if v.Tailnet.AuthURL != "" || v.Tailnet.State == "NeedsLogin" {
		m = append(m, entry{title: "Sign in to Tailscale…", click: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := t.Core.Login(ctx); err != nil {
				t.Logs.Printf("tray login: %v", err)
			}
		}})
	} else {
		m = append(m, entry{title: "Starting… (" + v.Tailnet.State + ")", disabled: true})
	}
	m = append(m, entry{sep: true})

	for _, r := range v.PairRequests {
		id := r.ID
		m = append(m, entry{title: "⚠ Pairing request: " + r.Name, children: []entry{
			{title: "From " + r.LoginName, disabled: true},
			{title: "Approve", click: func() { t.decide(id, true) }},
			{title: "Decline", click: func() { t.decide(id, false) }},
		}})
	}
	if len(v.PairRequests) > 0 {
		m = append(m, entry{sep: true})
	}

	var mine, disc []core.DeviceView
	for _, d := range v.Devices {
		switch d.Section {
		case core.SectionMine:
			mine = append(mine, d)
		case core.SectionDiscovered:
			disc = append(disc, d)
		}
	}
	if running {
		if len(mine) == 0 {
			m = append(m, entry{title: "No devices yet", disabled: true})
		}
		for _, d := range mine {
			m = append(m, entry{title: deviceTitle(d), children: t.deviceMenu(v, d)})
		}
		for _, d := range disc {
			id := d.ID
			title := "Pair with " + d.Name + "…"
			dis := false
			if d.Pairing == "requesting" || d.Pairing == "waiting" {
				title, dis = d.Name+" — waiting for approval", true
			}
			m = append(m, entry{title: title, disabled: dis || !d.Online, click: func() {
				if err := t.Core.PairDevice(id); err != nil {
					t.Logs.Printf("tray pair: %v", err)
				}
			}})
		}
		m = append(m, entry{sep: true})
	}
	m = append(m,
		entry{title: "Open TailConnect", click: func() { t.ShowUI("") }},
		entry{title: "Add device…", click: func() { t.ShowUI("#/add") }},
		entry{title: "Settings…", click: func() { t.ShowUI("#/settings") }},
		entry{sep: true},
		entry{title: "Quit TailConnect", click: func() { systray.Quit() }},
	)
	return m
}

func deviceTitle(d core.DeviceView) string {
	mark := "○"
	status := "offline"
	switch {
	case d.State == core.StateConnected:
		mark, status = "●", "connected"
	case d.State == core.StateConnecting || d.State == core.StateReconnecting:
		mark, status = "◐", d.State
	case d.State == core.StateError:
		mark, status = "✕", "failed"
	case d.Online:
		mark, status = "◌", "online"
	}
	return fmt.Sprintf("%s %s — %s", mark, d.Name, status)
}

func (t *Tray) deviceMenu(v core.View, d core.DeviceView) []entry {
	id := d.ID
	var m []entry
	if d.Link != "" && d.Online {
		m = append(m, entry{title: d.Link, disabled: true})
	}
	switch d.State {
	case core.StateConnected, core.StateConnecting, core.StateReconnecting:
		m = append(m, entry{title: "Disconnect", click: func() { t.Core.Disconnect(id) }})
	default:
		m = append(m, entry{title: "Connect", disabled: !d.Online, click: func() {
			if err := t.Core.Connect(id); err != nil {
				t.Logs.Printf("tray connect: %v", err)
			}
		}})
	}
	m = append(m, entry{sep: true})
	canLaunch := d.Online && (d.Kind != "peer" || (d.SSH && d.TrustsUs))
	for _, b := range v.Builtins {
		if !b.Enabled {
			continue
		}
		app := b.ID
		title := "Open " + b.Name
		if b.ID == "terminal" {
			title = "Open Terminal"
		}
		if !b.Available {
			title += " (not installed)"
		}
		m = append(m, entry{title: title, disabled: !b.Available || !canLaunch, click: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := t.Core.Launch(ctx, id, app); err != nil {
				t.Logs.Printf("tray launch %s: %v", app, err)
				t.ShowUI("#/device/" + id)
			}
		}})
	}
	if d.Kind == "peer" && d.Online && d.TrustsUs && !d.SSH {
		m = append(m, entry{title: "SSH is off on this device", disabled: true})
	}
	m = append(m, entry{title: "Resource Monitor", click: func() { t.ShowUI("#/device/" + id) }})
	if len(v.Apps) > 0 {
		m = append(m, entry{sep: true})
		runningApps := map[string]bool{}
		for _, r := range d.Running {
			runningApps[r.AppID] = true
		}
		for _, a := range v.Apps {
			app := a.ID
			if runningApps[app] {
				m = append(m, entry{title: fmt.Sprintf("■ Stop %s (:%d)", a.Name, a.Port), click: func() {
					if err := t.Core.StopApp(id, app); err != nil {
						t.Logs.Printf("tray stop app: %v", err)
					}
				}})
				continue
			}
			m = append(m, entry{title: fmt.Sprintf("▶ %s (:%d)", a.Name, a.Port), disabled: !d.Online, click: func() {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					if _, err := t.Core.StartApp(ctx, id, app); err != nil {
						t.Logs.Printf("tray start app: %v", err)
						t.ShowUI("#/device/" + id)
					}
				}()
			}})
		}
	}
	m = append(m, entry{sep: true}, entry{title: "Details…", click: func() { t.ShowUI("#/device/" + id) }})
	return m
}

func (t *Tray) decide(id string, approve bool) {
	if err := t.Core.ApprovePair(id, approve); err != nil {
		t.Logs.Printf("tray pair decision: %v", err)
	}
}

func signature(es []entry) string {
	var b strings.Builder
	var walk func([]entry, int)
	walk = func(es []entry, depth int) {
		for _, e := range es {
			fmt.Fprintf(&b, "%d|%s|%v|%v|%v\n", depth, e.title, e.disabled, e.sep, e.checked)
			walk(e.children, depth+1)
		}
	}
	walk(es, 0)
	return b.String()
}

func (t *Tray) rebuild() {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	v := t.Core.Snapshot(ctx)
	cancel()
	active := v.Tailnet.State == "Running"
	if t.active == nil || *t.active != active {
		setIcon(active)
		t.active = &active
		tip := "TailConnect"
		if !active {
			tip += " — not connected"
		}
		systray.SetTooltip(tip)
	}
	es := t.menu(v)
	sig := signature(es)
	if sig == t.sig {
		return
	}
	t.sig = sig
	if t.gen != nil {
		t.gen()
	}
	gctx, gcancel := context.WithCancel(context.Background())
	t.gen = gcancel
	systray.ResetMenu()
	t.add(gctx, nil, es)
}

func (t *Tray) add(ctx context.Context, parent *systray.MenuItem, es []entry) {
	for _, e := range es {
		if e.sep {
			if parent == nil {
				systray.AddSeparator()
			} else {
				parent.AddSeparator()
			}
			continue
		}
		var it *systray.MenuItem
		if parent == nil {
			it = systray.AddMenuItem(e.title, "")
		} else {
			it = parent.AddSubMenuItem(e.title, "")
		}
		if e.disabled {
			it.Disable()
		}
		if len(e.children) > 0 {
			t.add(ctx, it, e.children)
		}
		if e.click != nil {
			go func(it *systray.MenuItem, fn func()) {
				for {
					select {
					case <-ctx.Done():
						return
					case _, ok := <-it.ClickedCh:
						if !ok {
							return
						}
						go fn()
					}
				}
			}(it, e.click)
		}
	}
}
