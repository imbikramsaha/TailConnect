package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/autostart"
	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/paths"
	"github.com/imbikramsaha/tailconnect/internal/rustdesk"
	"github.com/imbikramsaha/tailconnect/internal/sshconf"
	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/ui"
	"github.com/imbikramsaha/tailconnect/internal/window"
)

// control is the running instance's advertisement in control.json.
type control struct {
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

func readControl() (*control, error) {
	p, err := paths.ControlFile()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c control
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func writeControl(c control) error {
	p, err := paths.ControlFile()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(c)
	return store.WriteFileAtomic(p, b, 0o600)
}

func removeControl(c control) {
	if cur, err := readControl(); err == nil && cur.PID == c.PID {
		if p, err := paths.ControlFile(); err == nil {
			os.Remove(p)
		}
	}
}

func (c *control) base() string { return "http://127.0.0.1:" + strconv.Itoa(c.Port) }

func (c *control) call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-TailConnect", "cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ping checks that the process on the advertised port is really our
// TailConnect (it must prove it knows the token) before the token is sent
// anywhere: after a crash, another local user could have taken the port.
func (c *control) ping() bool {
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return false
	}
	nonce := hex.EncodeToString(nb)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(c.base() + "/ping?n=" + nonce)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var r struct {
		MAC string `json:"mac"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r) != nil {
		return false
	}
	return hmac.Equal([]byte(r.MAC), []byte(ui.PingMAC(c.Token, nonce)))
}

// connectApp returns the running app's control handle, optionally starting
// the app in the background (so `ssh tc-device` works right after login).
func connectApp(autostart bool) (*control, error) {
	if c, err := readControl(); err == nil && c.ping() {
		return c, nil
	}
	if !autostart {
		return nil, errors.New("TailConnect is not running — start it first")
	}
	cmd := exec.Command(appExe(), "--background")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting TailConnect: %w", err)
	}
	go cmd.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if c, err := readControl(); err == nil && c.ping() {
			return c, nil
		}
	}
	return nil, errors.New("TailConnect did not start in time")
}

func runCLI(args []string) int {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return 0
	case "version":
		fmt.Println("tailconnect", version)
		return 0
	case "window":
		// Internal: the native window process (started by the app).
		if err := window.Run(os.Stdin); err != nil {
			return fatal(err)
		}
		return 0
	case "cleanup":
		// Used by uninstallers: remove paired devices' keys from
		// ~/.ssh/authorized_keys and TailConnect's Include from ~/.ssh/config.
		if err := cleanup(); err != nil {
			return fatal(err)
		}
		fmt.Println("Removed TailConnect's SSH keys and config entries.")
		return 0
	case "rustdesk-admin":
		// Internal: the elevated step of screen-sharing setup.
		if len(rest) != 1 {
			return 2
		}
		if err := rustdesk.AdminMain(rest[0]); err != nil {
			return fatal(err)
		}
		return 0
	case "proxy":
		if len(rest) != 2 {
			fmt.Fprintln(os.Stderr, "usage: tailconnect proxy <device> <port>")
			return 2
		}
		if err := proxy(rest[0], rest[1]); err != nil {
			fmt.Fprintln(os.Stderr, "tailconnect proxy:", err)
			return 1
		}
		return 0
	}
	c, err := connectApp(cmd == "open")
	if err != nil {
		return fatal(err)
	}
	switch cmd {
	case "open":
		route := ""
		if len(rest) > 0 {
			route = rest[0]
		}
		return check(c.call("POST", "/api/show", map[string]string{"route": route}, nil))
	case "quit":
		return check(c.call("POST", "/api/quit", struct{}{}, nil))
	case "screen":
		return screenCmd(c, rest)
	case "login":
		var r struct {
			AuthURL string `json:"authURL"`
		}
		if err := c.call("POST", "/api/login", struct{}{}, &r); err != nil {
			return fatal(err)
		}
		if r.AuthURL == "" {
			fmt.Println("Already signed in.")
		} else {
			fmt.Println("Sign in at:", r.AuthURL)
		}
		return 0
	case "logout":
		return check(c.call("POST", "/api/logout", struct{}{}, nil))
	case "status", "devices", "ls":
		var v core.View
		if err := c.call("GET", "/api/state", nil, &v); err != nil {
			return fatal(err)
		}
		if cmd == "status" {
			fmt.Printf("TailConnect %s on %s\n", v.Version, v.Self.Name)
			fmt.Printf("Tailscale: %s", v.Tailnet.State)
			if v.Tailnet.LoginName != "" {
				fmt.Printf(" as %s (%s, %s)", v.Tailnet.LoginName, v.Tailnet.Tailnet, v.Tailnet.IPv4)
			}
			fmt.Println()
			if v.Tailnet.AuthURL != "" {
				fmt.Println("Sign in at:", v.Tailnet.AuthURL)
			}
			if len(v.PairRequests) > 0 {
				fmt.Printf("%d pairing request(s) waiting — see `tailconnect pair list`\n", len(v.PairRequests))
			}
			fmt.Println()
		}
		printDevices(v)
		return 0
	case "connect", "disconnect":
		if len(rest) != 1 {
			fmt.Fprintf(os.Stderr, "usage: tailconnect %s <device>\n", cmd)
			return 2
		}
		d, err := resolve(c, rest[0])
		if err != nil {
			return fatal(err)
		}
		return check(c.call("POST", "/api/devices/"+url.PathEscape(d.ID)+"/"+cmd, struct{}{}, nil))
	case "ssh":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: tailconnect ssh <device> [ssh args]")
			return 2
		}
		d, err := resolve(c, rest[0])
		if err != nil {
			return fatal(err)
		}
		if d.Alias == "" {
			return fatal(errors.New("device is not paired yet"))
		}
		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			return fatal(errors.New("OpenSSH client (ssh) not found"))
		}
		sc := exec.Command(sshBin, append([]string{d.Alias}, rest[1:]...)...)
		sc.Stdin, sc.Stdout, sc.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := sc.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			return fatal(err)
		}
		return 0
	case "pair":
		return pairCmd(c, rest)
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
	usage()
	return 2
}

func check(err error) int {
	if err != nil {
		return fatal(err)
	}
	return 0
}

func printDevices(v core.View) {
	if len(v.Devices) == 0 {
		fmt.Println("No devices.")
		return
	}
	fmt.Printf("%-22s %-14s %-12s %-26s %s\n", "DEVICE", "ALIAS", "STATUS", "LINK", "SECTION")
	for _, d := range v.Devices {
		status := d.State
		if d.Section != core.SectionMine || d.State == core.StateDisconnected {
			status = "offline"
			if d.Online {
				status = "online"
			}
			if d.Pairing == "waiting" || d.Pairing == "requesting" {
				status = "pairing"
			}
		}
		alias := d.Alias
		if alias == "" {
			alias = "-"
		}
		fmt.Printf("%-22s %-14s %-12s %-26s %s\n", trunc(d.Name, 22), trunc(alias, 14), status, trunc(d.Link, 26), d.Section)
	}
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func resolve(c *control, q string) (core.DeviceView, error) {
	var v core.View
	if err := c.call("GET", "/api/state", nil, &v); err != nil {
		return core.DeviceView{}, err
	}
	var matches []core.DeviceView
	for _, d := range v.Devices {
		if d.ID == q || d.Alias == q {
			return d, nil
		}
		if strings.EqualFold(d.Name, q) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return core.DeviceView{}, fmt.Errorf("%q matches %d devices; use the alias or id", q, len(matches))
	}
	return core.DeviceView{}, fmt.Errorf("no device named %q (see `tailconnect devices`)", q)
}

func pairCmd(c *control, rest []string) int {
	if len(rest) == 0 || rest[0] == "list" {
		var v core.View
		if err := c.call("GET", "/api/state", nil, &v); err != nil {
			return fatal(err)
		}
		if len(v.PairRequests) == 0 {
			fmt.Println("No pending pairing requests.")
			return 0
		}
		for _, r := range v.PairRequests {
			fmt.Printf("%s  %s (%s, %s)\n", r.ID, r.Name, r.OS, r.LoginName)
		}
		return 0
	}
	usage := "usage: tailconnect pair list | approve <id> full|ports|stats | deny <id>"
	switch {
	case len(rest) == 3 && rest[0] == "approve":
		switch rest[2] {
		case "full", "ports", "stats":
		default:
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		return check(c.call("POST", "/api/pair-requests/"+url.PathEscape(rest[1])+"/approve", map[string]string{"access": rest[2]}, nil))
	case len(rest) == 2 && rest[0] == "deny":
		return check(c.call("POST", "/api/pair-requests/"+url.PathEscape(rest[1])+"/deny", struct{}{}, nil))
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

// proxy tunnels stdin/stdout to a device port through the running app.
func proxy(device, portStr string) error {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port %q", portStr)
	}
	c, err := connectApp(true)
	if err != nil {
		return err
	}
	var conn net.Conn
	var br *bufio.Reader
	deadline := time.Now().Add(25 * time.Second)
	for {
		conn, br, err = dialProxy(c, device, port)
		if err == nil {
			break
		}
		// Just-started apps need a moment to sign in and discover peers.
		if time.Now().After(deadline) || !strings.Contains(err.Error(), "offline") && !strings.Contains(err.Error(), "not connected") {
			return err
		}
		time.Sleep(time.Second)
	}
	defer conn.Close()
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, err = io.Copy(os.Stdout, br)
	return err
}

func dialProxy(c *control, device string, port int) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(c.Port), 5*time.Second)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{"device": {device}, "port": {strconv.Itoa(port)}}
	req, _ := http.NewRequest("GET", c.base()+"/api/proxy?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tailconnect-proxy")
	_ = conn.SetDeadline(time.Now().Add(40 * time.Second))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		conn.Close()
		return nil, nil, errors.New(strings.TrimSpace(string(msg)))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, nil
}

// cleanup undoes TailConnect's changes outside its own folders.
func cleanup() error {
	var errs []error
	if ak, err := sshkeys.AuthorizedKeysPath(); err == nil {
		errs = append(errs, sshkeys.RevokeAll(ak))
	}
	if d, err := paths.UserSSHDir(); err == nil {
		if sd, err := paths.SSHDir(); err == nil {
			errs = append(errs, sshconf.RemoveInclude(filepath.Join(d, "config"), filepath.Join(sd, "config")))
		}
	}
	errs = append(errs, autostart.Set(false, ""))
	return errors.Join(errs...)
}

// screenCmd: "screen setup" installs and configures RustDesk for screen
// sharing (the app asks for administrator rights); "screen status" shows it.
func screenCmd(c *control, args []string) int {
	type screenState struct {
		Screen core.ScreenView `json:"screen"`
	}
	get := func() (core.ScreenView, error) {
		var v screenState
		err := c.call("GET", "/api/state", nil, &v)
		return v.Screen, err
	}
	show := func(s core.ScreenView) {
		switch {
		case s.Configured && s.PasswordKnown:
			fmt.Println("Screen sharing: RustDesk is set up; paired devices connect without typing a password.")
		case s.Configured:
			fmt.Println("Screen sharing: RustDesk is set up; enter RustDesk's password when connecting.")
		case s.RustDeskInstalled:
			fmt.Println("Screen sharing: RustDesk is installed but not set up for TailConnect (run: tailconnect screen setup).")
		default:
			fmt.Println("Screen sharing: RustDesk isn't installed (run: tailconnect screen setup).")
		}
		if s.Configured && s.Locked {
			fmt.Println("RustDesk accepts connections only through TailConnect.")
		}
		if s.Error != "" {
			fmt.Println("Last setup problem:", s.Error)
		}
	}
	if len(args) == 0 || args[0] == "status" {
		s, err := get()
		if err != nil {
			return fatal(err)
		}
		show(s)
		return 0
	}
	if args[0] != "setup" {
		fmt.Fprintln(os.Stderr, "usage: tailconnect screen [status|setup]")
		return 2
	}
	if err := c.call("POST", "/api/screen/setup", struct{}{}, nil); err != nil {
		return fatal(err)
	}
	fmt.Println("Setting up RustDesk for screen sharing — confirm the administrator prompt…")
	for {
		time.Sleep(time.Second)
		s, err := get()
		if err != nil {
			return fatal(err)
		}
		if !s.Running {
			show(s)
			if s.Error != "" {
				return 1
			}
			if runtime.GOOS == "darwin" {
				fmt.Println("On this Mac, allow RustDesk under System Settings → Privacy & Security → Screen Recording and Accessibility.")
			}
			return 0
		}
	}
}
