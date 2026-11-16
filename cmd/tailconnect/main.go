// Command tailconnect is TailConnect: a tray app that connects all your
// devices over an embedded Tailscale node — one-click terminals, editors,
// port forwards and live resource monitoring. With a subcommand it acts as
// a CLI for the running app (see `tailconnect help`).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/client/local"

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/hostname"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/logx"
	"github.com/imbikramsaha/tailconnect/internal/notify"
	"github.com/imbikramsaha/tailconnect/internal/paths"
	"github.com/imbikramsaha/tailconnect/internal/sshconf"
	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
	"github.com/imbikramsaha/tailconnect/internal/tray"
	"github.com/imbikramsaha/tailconnect/internal/ui"
	"github.com/imbikramsaha/tailconnect/internal/window"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		os.Exit(runCLI(args))
	}
	fs := flag.NewFlagSet("tailconnect", flag.ExitOnError)
	headless := fs.Bool("headless", false, "run without tray or window (servers); prints the sign-in URL")
	background := fs.Bool("background", false, "start in the tray without opening the window")
	authKey := fs.String("authkey", "", "Tailscale auth key (visible to other users in the process list; prefer --authkey-file or TS_AUTHKEY)")
	authKeyFile := fs.String("authkey-file", "", "read the Tailscale auth key from this file")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = usage
	_ = fs.Parse(args)
	if *showVersion {
		fmt.Println("tailconnect", version)
		return
	}
	if !*headless && runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		*headless = true // no desktop session (e.g. over SSH): run as a background agent
	}
	if *authKeyFile != "" {
		b, err := os.ReadFile(*authKeyFile)
		if err != nil {
			os.Exit(fatal(err))
		}
		*authKey = strings.TrimSpace(string(b))
	}
	os.Exit(runApp(*headless, *background, *authKey))
}

func usage() {
	fmt.Fprint(os.Stderr, `TailConnect — connect all your devices over Tailscale.

Usage:
  tailconnect [--background] [--headless] [--authkey KEY]   run the app
  tailconnect <command> [args]                               talk to the running app

Commands:
  open [route]            open the TailConnect window
  status                  show sign-in state and devices
  devices                 list devices
  connect <device>        connect to a device (name, alias or id)
  disconnect <device>     disconnect from a device
  ssh <device> [args]     open an SSH session to a device
  login | logout          sign in to / out of Tailscale
  pair list               list pending pairing requests
  pair approve <id> <full|ports|stats>
                          approve a pairing request with that access
  pair deny <id>          decline a pairing request
  screen setup            install and set up RustDesk for screen sharing
  screen status           show screen-sharing setup
  proxy <device> <port>   stdio tunnel to a device port (used by ssh ProxyCommand)
  cleanup                 remove TailConnect's SSH keys/config entries (for uninstalling)
  quit                    quit the running app
  version                 print the version
`)
}

func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "tailconnect"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return exe
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// waitForPID waits (briefly) for a previous instance to exit after a restart.
func waitForPID() {
	pid, err := strconv.Atoi(os.Getenv("TAILCONNECT_WAIT_PID"))
	if err != nil || pid <= 0 {
		return
	}
	os.Unsetenv("TAILCONNECT_WAIT_PID")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := readControl(); err != nil || c.PID != pid || !c.ping() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// relaunch starts a new instance that waits for this one to exit.
func relaunch(quit func()) error {
	cmd := exec.Command(appExe(), "--background")
	cmd.Env = append(os.Environ(), "TAILCONNECT_WAIT_PID="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Process.Release()
	go func() {
		time.Sleep(300 * time.Millisecond)
		quit()
	}()
	return nil
}

func runApp(headless, background bool, authKey string) int {
	waitForPID()
	if c, err := readControl(); err == nil && c.ping() {
		if headless {
			fmt.Fprintln(os.Stderr, "TailConnect is already running.")
			return 1
		}
		if !background {
			_ = c.call("POST", "/api/show", map[string]string{"route": ""}, nil)
		}
		return 0
	}

	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return fatal(err)
	}
	logDir, err := paths.LogDir()
	if err != nil {
		return fatal(err)
	}
	var echo io.Writer
	if headless {
		echo = os.Stderr
	}
	logs, err := logx.Open(logDir, echo)
	if err != nil {
		return fatal(err)
	}
	defer logs.Close()

	statePath, _ := paths.StateFile()
	st, err := store.Open(statePath)
	if err != nil {
		return fatal(fmt.Errorf("reading %s: %w", statePath, err))
	}
	sshDir, err := paths.SSHDir()
	if err != nil {
		return fatal(err)
	}
	tsDir, err := paths.TSNetDir()
	if err != nil {
		return fatal(err)
	}
	settings := st.Get().Settings
	host := settings.Hostname
	if host == "" {
		host = hostname.Friendly()
	}
	if authKey == "" {
		authKey = os.Getenv("TS_AUTHKEY")
	}
	// Use the Tailscale app if it's already running and signed in when
	// TailConnect is first set up (no second sign-in); existing installs
	// keep their own node, since switching changes the device's identity.
	mode := settings.TailscaleMode
	if mode == "" {
		mode = tailnet.ModeEmbedded
		if !settings.Onboarded && authKey == "" && settings.ControlURL == "" &&
			tailnet.DetectSystem(context.Background()) != nil {
			mode = tailnet.ModeApp
		}
		_ = st.Update(func(s *store.State) error { s.Settings.TailscaleMode = mode; return nil })
		settings.TailscaleMode = mode
	}
	var system *local.Client
	if mode == tailnet.ModeApp {
		system = &local.Client{}
	}
	authorized, err := sshkeys.AuthorizedKeysPath()
	if os.Getenv(paths.EnvHome) != "" {
		authorized = filepath.Join(cfgDir, "authorized_keys") // isolated test instance
	} else if err != nil {
		return fatal(err)
	}
	userSSHConfig := ""
	if paths.ManagesUserSSHConfig() {
		if d, err := paths.UserSSHDir(); err == nil {
			userSSHConfig = filepath.Join(d, "config")
		}
	}

	c, err := core.New(core.Options{
		Version: version,
		Logs:    logs,
		Store:   st,
		Tailnet: tailnet.Config{
			Dir:        tsDir,
			Hostname:   sshconf.Alias(host), // e.g. "tc-my-laptop" in the admin console
			ControlURL: settings.ControlURL,
			AuthKey:    authKey,
			System:     system,
			Logf:       logs.Verbose.Printf,
			UserLogf:   logs.App.Printf,
		},
		Exe:            cliExe(),
		AppExe:         appExe(),
		SSHDir:         sshDir,
		AuthorizedKeys: authorized,
		UserSSHConfig:  userSSHConfig,
		Headless:       headless,
		InstallWarning: installWarning(executable()),
		Notify:         notifier(headless),
	})
	if err != nil {
		return fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		return fatal(err)
	}

	ln, err := ui.Listen()
	if err != nil {
		return fatal(fmt.Errorf("control API: %w", err))
	}
	srv := &ui.Server{Core: c, Token: randomToken(), Logs: logs}
	srv.Bind(ln)
	go func() {
		if err := srv.Serve(ln); err != nil {
			logs.Printf("control API: %v", err)
		}
	}()
	ctl := control{Port: srv.Port(), Token: srv.Token, PID: os.Getpid(), Version: version}
	if err := writeControl(ctl); err != nil {
		logs.Printf("control file: %v", err)
	}
	defer removeControl(ctl)
	logs.Printf("TailConnect %s ready (control API on 127.0.0.1:%d)", version, srv.Port())

	profile := filepath.Join(cfgDir, "ui-profile")
	win := &windowProc{exe: appExe(), logf: logs.Printf}
	showUI := func(route string) {
		if headless {
			return
		}
		if _, ok := window.Command(win.exe); ok {
			if err := win.show(srv.LoginURL(route)); err == nil {
				return
			} else {
				logs.Printf("native window: %v; using browser", err)
			}
		}
		// Browsers take URLs on the command line, where other local users
		// can read them: hand over a private file that redirects instead.
		target, cleanup, err := privateLauncher(cfgDir, srv.LoginURL(route))
		if err != nil {
			logs.Printf("open window: %v", err)
			return
		}
		time.AfterFunc(time.Minute, cleanup)
		if err := launch.AppWindow(target, profile); err != nil {
			logs.Printf("open window: %v", err)
		}
	}
	srv.ShowUI = showUI
	srv.Restart = func() error {
		if headless {
			return errors.New("restart the service to apply changes")
		}
		return relaunch(func() { srv.Quit() })
	}
	notify.OnClick(showUI)
	defer win.close()

	quit := make(chan struct{})
	var once sync.Once
	doQuit := func() { once.Do(func() { close(quit) }) }
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigc
		doQuit()
	}()

	if headless {
		srv.Quit = doQuit
		go announce(c)
		<-quit
	} else {
		tr := &tray.Tray{Core: c, Logs: logs, ShowUI: showUI}
		srv.Quit = tray.Quit
		go func() {
			<-quit
			tray.Quit()
		}()
		if !background || !settings.Onboarded {
			go func() {
				time.Sleep(400 * time.Millisecond)
				showUI("")
			}()
			_ = st.Update(func(s *store.State) error { s.Settings.Onboarded = true; return nil })
		}
		tr.Run()
	}
	logs.Printf("shutting down")
	_ = srv.Close()
	c.Close()
	return 0
}

// announce prints sign-in instructions and status changes in headless mode.
func announce(c *core.Core) {
	ch, unsub := c.Events().Subscribe()
	defer unsub()
	lastURL, lastState := "", ""
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		inf := c.Node().Info(context.Background())
		if inf.AuthURL != "" && inf.AuthURL != lastURL {
			fmt.Fprintf(os.Stderr, "\nTo sign this device in to Tailscale, visit:\n\n\t%s\n\n", inf.AuthURL)
			lastURL = inf.AuthURL
		}
		if inf.State != lastState {
			if inf.State == tailnet.StateRunning {
				fmt.Fprintf(os.Stderr, "Connected to tailnet %q as %s (%s). Devices on your account can now pair with %s.\n",
					inf.Tailnet, inf.LoginName, inf.IPv4, c.Name())
			}
			lastState = inf.State
		}
		select {
		case <-ch:
		case <-tick.C:
		}
	}
}

func fatal(err error) int {
	fmt.Fprintln(os.Stderr, "tailconnect:", err)
	return 1
}

// installWarning flags locations the app should not run from: macOS runs
// quarantined apps from a randomised read-only copy (App Translocation), and
// a mounted DMG disappears — either breaks the ssh ProxyCommand path.
func installWarning(exe string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	if strings.Contains(exe, "/AppTranslocation/") || strings.HasPrefix(exe, "/Volumes/") {
		return "TailConnect is running from a temporary location. Quit it, drag TailConnect to your Applications folder, and open it from there."
	}
	return ""
}

func notifier(headless bool) func(title, body, route string) error {
	if headless {
		return func(title, body, _ string) error {
			fmt.Fprintf(os.Stderr, "%s: %s\n", title, body)
			return nil
		}
	}
	return notify.Send
}

// windowProc manages the single native window process.
type windowProc struct {
	exe  string
	logf func(string, ...any)

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
}

// show opens the window at url, or navigates/raises the existing one.
func (w *windowProc) show(url string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd != nil {
		select {
		case <-w.done:
			w.cmd = nil
		default:
			if _, err := io.WriteString(w.stdin, url+"\n"); err == nil {
				return nil
			}
			w.cmd = nil
		}
	}
	argv, ok := window.Command(w.exe)
	if !ok {
		return window.ErrUnsupported
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		if err := cmd.Wait(); err != nil {
			w.logf("window exited: %v", err)
		}
		close(done)
	}()
	if _, err := io.WriteString(in, url+"\n"); err != nil {
		return err
	}
	w.cmd, w.stdin, w.done = cmd, in, done
	return nil
}

// close closes the window (it exits when its stdin closes).
func (w *windowProc) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stdin != nil {
		w.stdin.Close()
	}
}

// privateLauncher writes an owner-only HTML file that redirects to url and
// returns its file:// URL, so the one-time sign-in link never appears in a
// browser's command line.
func privateLauncher(dir, target string) (string, func(), error) {
	f, err := os.CreateTemp(dir, "open-*.html")
	if err != nil {
		return "", nil, err
	}
	esc := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(target)
	_, err = fmt.Fprintf(f, `<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="refresh" content="0;url=%s"><title>TailConnect</title>`, esc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o600)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	abs, _ := filepath.Abs(f.Name())
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if runtime.GOOS == "windows" {
		u.Path = "/" + u.Path
	}
	return u.String(), func() { os.Remove(f.Name()) }, nil
}
