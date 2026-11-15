package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/paths"
	"github.com/imbikramsaha/tailconnect/internal/store"
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

func (c *control) ping() bool {
	req, _ := http.NewRequest("GET", c.base()+"/api/ping", nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
	cmd := exec.Command(executable(), "--background")
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
	if len(rest) != 2 || (rest[0] != "approve" && rest[0] != "deny") {
		fmt.Fprintln(os.Stderr, "usage: tailconnect pair list | approve <id> | deny <id>")
		return 2
	}
	return check(c.call("POST", "/api/pair-requests/"+url.PathEscape(rest[1])+"/"+rest[0], struct{}{}, nil))
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
