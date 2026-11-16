package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/logx"
	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
	"github.com/imbikramsaha/tailconnect/internal/sshx"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
)

// These tests run two real TailConnect cores, each with its own embedded
// Tailscale node, against an in-process coordination server and DERP relay.

func startControl(t *testing.T, sameUser bool) string {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	control := &testcontrol.Server{
		DERPMap:          derpMap,
		DNSConfig:        &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain:   "tail-test.ts.net",
		AllNodesSameUser: sameUser,
		AllOnline:        true, // real control reports presence; the fake needs a hint
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control.HTTPTestServer.URL
}

// testWindowed makes the next cores act as desktop (not headless) apps.
var testWindowed bool

func newCore(t *testing.T, controlURL, name string, autoTrust bool) *Core {
	t.Helper()
	return newCoreWith(t, controlURL, name, autoTrust, nil)
}

// newCoreWith creates a core; system, if set, makes it use that Tailscale
// daemon (the "Tailscale app" mode) instead of an embedded node.
func newCoreWith(t *testing.T, controlURL, name string, autoTrust bool, system *local.Client) *Core {
	t.Helper()
	// CI runners (Windows) run as an elevated administrator, where built-in
	// SSH otherwise refuses to start.
	t.Setenv("TAILCONNECT_ALLOW_ELEVATED_SSH", "1")
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *store.State) error {
		s.Settings.Hostname = name
		s.Settings.AutoTrustOwnDevices = autoTrust
		s.Settings.ManageSSHConfig = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tsDir := filepath.Join(dir, "tsnet")
	os.MkdirAll(tsDir, 0o700)
	tw := testWriter{t, name}
	logs := &logx.Logs{
		App:     log.New(tw, "app: ", 0),
		Session: log.New(tw, "session: ", 0),
		Verbose: log.New(io.Discard, "", 0),
	}
	c, err := New(Options{
		Version: "test",
		Logs:    logs,
		Store:   st,
		Tailnet: tailnet.Config{
			Dir: tsDir, Hostname: name, ControlURL: controlURL, Ephemeral: true, InMemory: true,
			UserLogf: logs.App.Printf,
			System:   system,
		},
		Exe:               "tailconnect",
		SSHDir:            filepath.Join(dir, "ssh"),
		AuthorizedKeys:    filepath.Join(dir, "authorized_keys"),
		OpenURL:           func(string) error { return nil },
		DiscoveryInterval: 250 * time.Millisecond,
		ProbeTailConnect:  time.Second,
		ProbeOther:        time.Second,
		Headless:          !testWindowed,
		InboxDir:          filepath.Join(dir, "inbox"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { c.Close(); cancel() })
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

type testWriter struct {
	t    *testing.T
	name string
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("[%s] %s", w.name, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// dump logs a core's view (for failures).
func dump(t *testing.T, c *Core) {
	v := c.Snapshot(context.Background())
	t.Logf("[%s] tailnet=%+v", c.Name(), v.Tailnet)
	for _, d := range v.Devices {
		t.Logf("[%s]   device %s section=%s online=%v tc=%v trustsUs=%v pairing=%s/%s",
			c.Name(), d.Name, d.Section, d.Online, d.TailConnect, d.TrustsUs, d.Pairing, d.PairingMsg)
	}
	c.mu.Lock()
	for id, p := range c.peers {
		t.Logf("[%s]   peer %s %s ip=%s online=%v probeErr=%q", c.Name(), id, p.HostName, p.IP, p.Online, p.ProbeErr)
	}
	c.mu.Unlock()
}

var dumpOnFail []*Core

// waitFor polls cond until it returns true or the deadline passes.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, c := range dumpOnFail {
		dump(t, c)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// deviceNamed returns c's view of the device called name.
func deviceNamed(c *Core, name string) (DeviceView, bool) {
	v := c.Snapshot(context.Background())
	for _, d := range v.Devices {
		if d.Name == name {
			return d, true
		}
	}
	return DeviceView{}, false
}

func TestAutoPairTunnelStatsAndRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	url := startControl(t, true)
	a := newCore(t, url, "alpha", true)
	b := newCore(t, url, "bravo", true)
	dumpOnFail = []*Core{a, b}

	// Same account → both sides pair automatically, in both directions.
	waitFor(t, "alpha and bravo to auto-pair", 60*time.Second, func() bool {
		da, ok1 := deviceNamed(a, "bravo")
		db, ok2 := deviceNamed(b, "alpha")
		return ok1 && ok2 && da.Section == SectionMine && db.Section == SectionMine &&
			da.TrustsUs && db.TrustsUs && da.TrustedByUs && db.TrustedByUs
	})
	devB, _ := deviceNamed(a, "bravo")
	if devB.Alias != "tc-bravo" {
		t.Errorf("alias = %q, want tc-bravo", devB.Alias)
	}

	// The managed ssh config routes bravo through `tailconnect proxy`.
	cfg, err := os.ReadFile(a.managedConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "Host tc-bravo") || !strings.Contains(string(cfg), "proxy "+devB.ID+" %p") {
		t.Errorf("ssh config missing bravo entry:\n%s", cfg)
	}
	// bravo installed alpha's key in its authorized_keys.
	ak, _ := os.ReadFile(b.opts.AuthorizedKeys)
	if !strings.Contains(string(ak), a.Identity().PublicLine) {
		t.Errorf("alpha's key not authorized on bravo:\n%s", ak)
	}

	// A device must never accept itself as a caller (with the Tailscale app
	// in TUN mode other local users could otherwise pose as an "own device").
	selfIP := a.Snapshot(context.Background()).Tailnet.IPv4
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, _, err = a.agentCli.Hello(sctx, selfIP)
	var se *agent.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("self call: want 403 from the agent, got %v", err)
	}
	if r, err := a.agentCli.Pair(sctx, selfIP, agent.PairRequest{Name: "x", PublicKey: a.Identity().PublicLine}); err == nil && r.Status == agent.PairApproved {
		t.Error("agent auto-paired with its own node")
	}
	scancel()

	// Connect.
	if err := a.Connect(devB.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", 30*time.Second, func() bool {
		d, _ := deviceNamed(a, "bravo")
		return d.State == StateConnected
	})
	// Bravo's signed answers pinned its real device key on alpha.
	if d, ok := a.device(devB.ID); !ok || d.PeerKey != b.Identity().PublicLine {
		t.Error("bravo's device key was not pinned from its signed answers")
	}

	// A web service "on bravo" (loopback of this test process).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from bravo")
	})}
	go srv.Serve(ln)
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Custom app forwarding that port. The port is taken locally (same
	// machine), so the forward must fall back to a free local port.
	app, err := a.SaveApp(store.App{Name: "Web", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run, err := a.StartApp(ctx, devB.ID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.LocalPort == port {
		t.Fatalf("expected a different local port on collision, got %d", run.LocalPort)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", run.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from bravo" {
		t.Fatalf("tunnel body = %q", body)
	}

	// Resource monitor over the agent API.
	stats, err := a.Stats(ctx, devB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MemTotal == 0 || len(stats.PerCore) == 0 {
		t.Errorf("implausible stats: %+v", stats)
	}

	if err := a.StopApp(devB.ID, app.ID); err != nil {
		t.Fatal(err)
	}

	// Removing bravo unpairs both ways and is not undone by auto-pairing.
	if err := a.RemoveDevice(ctx, devB.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bravo to forget alpha", 10*time.Second, func() bool {
		return !b.IsTrusted(a.Snapshot(ctx).Tailnet.NodeID)
	})
	time.Sleep(3 * time.Second) // several discovery cycles
	if d, _ := deviceNamed(a, "bravo"); d.Section == SectionMine {
		t.Fatalf("bravo was re-paired after removal")
	}
}

func TestManualPairingApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	url := startControl(t, false) // different users → approval required
	a := newCore(t, url, "alpha", true)
	b := newCore(t, url, "bravo", true)
	dumpOnFail = []*Core{a, b}

	var discovered DeviceView
	waitFor(t, "alpha to discover bravo", 60*time.Second, func() bool {
		d, ok := deviceNamed(a, "bravo")
		discovered = d
		return ok && d.Section == SectionDiscovered
	})
	if err := a.PairDevice(discovered.ID); err != nil {
		t.Fatal(err)
	}
	var reqID string
	waitFor(t, "bravo to receive a pairing request", 20*time.Second, func() bool {
		v := b.Snapshot(context.Background())
		if len(v.PairRequests) == 1 {
			reqID = v.PairRequests[0].ID
			return v.PairRequests[0].Name == "alpha" && !v.PairRequests[0].SameUser
		}
		return false
	})
	waitFor(t, "alpha to show the pairing code", 10*time.Second, func() bool {
		d, _ := deviceNamed(a, "bravo")
		return d.Pairing == "waiting" && d.PairCode != ""
	})
	da, _ := deviceNamed(a, "bravo")
	if reqs := b.Snapshot(context.Background()).PairRequests; reqs[0].Code != da.PairCode {
		t.Errorf("pairing codes differ: requester %q, approver %q", da.PairCode, reqs[0].Code)
	}
	if st := b.store.Get(); len(st.Pending) != 1 {
		t.Errorf("pending request not persisted: %+v", st.Pending)
	}
	if err := b.ApprovePair(reqID, true, "stats"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alpha to save bravo", 20*time.Second, func() bool {
		d, ok := deviceNamed(a, "bravo")
		return ok && d.Section == SectionMine && d.TrustsUs
	})
	// Approving offers the reverse pairing; alpha must approve it too.
	waitFor(t, "reverse request on alpha", 20*time.Second, func() bool {
		v := a.Snapshot(context.Background())
		if len(v.PairRequests) == 1 {
			reqID = v.PairRequests[0].ID
			return true
		}
		return false
	})
	if err := a.ApprovePair(reqID, true, "full"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bravo to save alpha", 20*time.Second, func() bool {
		d, ok := deviceNamed(b, "alpha")
		return ok && d.Section == SectionMine && d.TrustsUs
	})
	// Another process on alpha's machine can't take over its pairing by
	// presenting a new key: that needs a fresh approval.
	pctx, pcancel := context.WithTimeout(context.Background(), 10*time.Second)
	bIP := func() string { d, _ := deviceNamed(a, "bravo"); return d.TailnetIP }()
	ik, _ := sshkeys.LoadOrCreate(t.TempDir(), "intruder")
	resp, err := agent.NewClient(a.node.Dial, ik.Signer).Pair(pctx, bIP, agent.PairRequest{Name: "alpha", PublicKey: ik.PublicLine})
	pcancel()
	if err != nil || resp.Status != agent.PairPending {
		t.Fatalf("key swap: want a pending approval, got %+v %v", resp, err)
	}
	// Pairing with a key you don't hold is refused outright.
	pctx, pcancel = context.WithTimeout(context.Background(), 10*time.Second)
	_, err = agent.NewClient(a.node.Dial, ik.Signer).Pair(pctx, bIP, agent.PairRequest{Name: "x", PublicKey: b.Identity().PublicLine})
	var se *agent.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("pair request for someone else's key: want 403, got %v", err)
	}
	pcancel()

	// bravo granted alpha monitor-only access.
	waitFor(t, "alpha to learn its access level", 10*time.Second, func() bool {
		d, _ := deviceNamed(a, "bravo")
		return d.Access == "stats"
	})
	devB, _ := deviceNamed(a, "bravo")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.Stats(ctx, devB.ID); err != nil {
		t.Errorf("stats with stats access: %v", err)
	}
	if _, err := a.Services(ctx, devB.ID); err == nil {
		t.Error("services allowed with stats access")
	}
	if c, err := a.DialDevice(ctx, devB.ID, 22); err == nil {
		c.Close()
		t.Error("tunnel allowed with stats access")
	}
	if cl, err := sshx.Connect(ctx, a.sshParams(a.mustDevice(t, devB.ID), "")); err == nil {
		cl.Close()
		t.Error("built-in SSH allowed with stats access")
	}
	if d, _ := deviceNamed(a, "bravo"); d.BuiltinSSH {
		t.Error("built-in SSH port revealed to a stats-only device")
	}
}

func (c *Core) mustDevice(t *testing.T, id string) store.Device {
	d, ok := c.device(id)
	if !ok {
		t.Fatalf("no device %s", id)
	}
	return d
}

// otherProcess returns an agent client on c's tailnet node but with its own
// key: another program or OS user sharing that machine's Tailscale identity.
func otherProcess(t *testing.T, c *Core) *agent.Client {
	t.Helper()
	k, err := sshkeys.LoadOrCreate(t.TempDir(), "intruder")
	if err != nil {
		t.Fatal(err)
	}
	return agent.NewClient(c.node.Dial, k.Signer)
}

func freePort(t *testing.T) (net.Listener, int) {
	// A fixed range below the ephemeral ports (which service detection skips).
	for p := 20000 + int(time.Now().UnixNano()%10000); p < 40000; p++ {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			return ln, p
		}
	}
	t.Fatal("no free port")
	return nil, 0
}

func TestBuiltinSSHServicesFilesAndAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	url := startControl(t, true)
	a := newCore(t, url, "alpha", true)
	b := newCore(t, url, "bravo", true)
	dumpOnFail = []*Core{a, b}
	waitFor(t, "auto-pair with built-in SSH", 60*time.Second, func() bool {
		d, ok := deviceNamed(a, "bravo")
		return ok && d.Section == SectionMine && d.TrustsUs && d.BuiltinSSH && d.Access == "full"
	})
	devB, _ := deviceNamed(a, "bravo")
	if d := a.mustDevice(t, devB.ID); d.Port != agent.SSHPort {
		t.Fatalf("device SSH port = %d, want built-in %d", d.Port, agent.SSHPort)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Built-in SSH: a real SSH session through the tailnet, no sshd needed.
	cl, err := sshx.Connect(ctx, a.sshParams(a.mustDevice(t, devB.ID), ""))
	if err != nil {
		t.Fatalf("built-in SSH: %v", err)
	}
	out, err := sshx.Run(ctx, cl, "echo built-in-ok", nil)
	cl.Close()
	if err != nil || strings.TrimSpace(out) != "built-in-ok" {
		t.Fatalf("ssh run: %q %v", out, err)
	}

	// Service detection + one-click forward. The service must be another
	// process: TailConnect hides its own listening ports.
	ln, port := freePort(t)
	ln.Close()
	webDir := t.TempDir()
	os.WriteFile(filepath.Join(webDir, "index.html"), []byte("svc"), 0o644)
	py := "python3"
	if _, err := exec.LookPath(py); err != nil {
		py = "python"
	}
	web := exec.Command(py, "-m", "http.server", strconv.Itoa(port), "--bind", "127.0.0.1", "--directory", webDir)
	if err := web.Start(); err != nil {
		t.Skipf("python3 not available: %v", err)
	}
	defer web.Process.Kill()
	var svcs []agent.Service
	waitFor(t, "service detection", 15*time.Second, func() bool {
		svcs, _ = a.Services(ctx, devB.ID)
		for _, s := range svcs {
			if s.Port == port && s.Web {
				return true
			}
		}
		return false
	})
	run, err := a.Forward(ctx, devB.ID, port, "Test", false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", run.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "svc" {
		t.Fatalf("forward body %q", body)
	}

	// Processes + kill.
	sleeper := exec.Command("sleep", "120")
	if runtime.GOOS == "windows" {
		sleeper = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 120")
	}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	if ps, err := a.Processes(ctx, devB.ID); err != nil || len(ps) == 0 {
		t.Fatalf("processes: %d %v", len(ps), err)
	}
	if err := a.Kill(ctx, devB.ID, sleeper.Process.Pid); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- sleeper.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("process was not stopped")
	}

	// Files land in bravo's inbox without overwriting.
	for i := 0; i < 2; i++ {
		if _, err := a.SendFile(ctx, devB.ID, "../notes.txt", 5, strings.NewReader("hello")); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"notes.txt", "notes (1).txt"} {
		if b2, err := os.ReadFile(filepath.Join(b.opts.InboxDir, n)); err != nil || string(b2) != "hello" {
			t.Errorf("inbox %s: %q %v", n, b2, err)
		}
	}

	// Diagnostics measure latency and throughput.
	diag, err := a.Diagnose(ctx, devB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(diag.LatencyMS) == 0 || diag.SpeedMbps <= 0 || diag.Path == "" {
		t.Errorf("diagnosis: %+v", diag)
	}

	// Another process on alpha's machine (same tailnet identity, different
	// key) gets nothing: requests must be signed with the paired key.
	bIP := devB.TailnetIP
	intruder := otherProcess(t, a)
	_, err = intruder.Stats(ctx, bIP)
	var se *agent.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("foreign-key request: want 403, got %v", err)
	}
	if h, _, err := intruder.Hello(ctx, bIP); err != nil || h.Trusted || h.User != "" || h.HostKey != "" {
		t.Errorf("foreign-key hello revealed paired details: %+v %v", h, err)
	}
	if _, err := agent.NewClient(a.node.Dial, nil).Stats(ctx, bIP); err == nil {
		t.Error("unsigned request was accepted")
	}

	// An open tunnel is closed when access is lowered.
	tun, err := a.agentCli.Tunnel(ctx, bIP, port)
	if err != nil {
		t.Fatal(err)
	}

	// Lowering access to "ports" cuts SSH but keeps forwards.
	alphaNode := a.Snapshot(ctx).Tailnet.NodeID
	if err := b.SetAccess(alphaNode, "ports", nil); err != nil {
		t.Fatal(err)
	}
	_ = tun.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := tun.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("tunnel stayed open after access was lowered: %v", err)
	}
	tun.Close()
	if cl, err := sshx.Connect(ctx, a.sshParams(a.mustDevice(t, devB.ID), "")); err == nil {
		cl.Close()
		t.Error("built-in SSH still allowed with ports access")
	}
	if _, err := a.Services(ctx, devB.ID); err != nil {
		t.Errorf("services with ports access: %v", err)
	}
	ak, _ := os.ReadFile(b.opts.AuthorizedKeys)
	if strings.Contains(string(ak), a.Identity().PublicLine) {
		t.Error("authorized_keys still has the key after lowering access")
	}
	// Port allow-list.
	if err := b.SetAccess(alphaNode, "ports", []int{port + 1}); err != nil {
		t.Fatal(err)
	}
	if c, err := a.DialDevice(ctx, devB.ID, port); err == nil {
		c.Close()
		t.Error("tunnel to a port outside the allow-list succeeded")
	}
}

// buildTailscale builds tailscaled and the tailscale CLI from the module
// cache (with Tailscale's own go.mod), cached across test runs.
func buildTailscale(t *testing.T) (daemon, cli string) {
	t.Helper()
	modDir, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "tailscale.com").Output()
	if err != nil {
		t.Skipf("can't locate tailscale.com module: %v", err)
	}
	ver, _ := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "tailscale.com").Output()
	dir := filepath.Join(os.TempDir(), "tailconnect-test-tailscale-"+strings.TrimSpace(string(ver)))
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	daemon, cli = filepath.Join(dir, "tailscaled"+exe), filepath.Join(dir, "tailscale"+exe)
	if _, err := os.Stat(cli); err == nil {
		return daemon, cli
	}
	os.MkdirAll(dir, 0o755)
	for _, pkg := range []string{"./cmd/tailscaled", "./cmd/tailscale"} {
		out := daemon
		if strings.HasSuffix(pkg, "/tailscale") {
			out = cli
		}
		cmd := exec.Command("go", "build", "-o", out, pkg)
		cmd.Dir = strings.TrimSpace(string(modDir))
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", pkg, err, b)
		}
	}
	return daemon, cli
}

// TestTailscaleAppMode runs one device through a real tailscaled in
// userspace-networking mode (standing in for the Tailscale app) and the other
// on an embedded node, and checks they pair and reach each other both ways.
func TestTailscaleAppMode(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if runtime.GOOS == "windows" {
		t.Skip("tailscaled runs as a service on Windows; covered on macOS/Linux")
	}
	daemon, cli := buildTailscale(t)
	url := startControl(t, true)
	dir := t.TempDir()
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("tc-%d.sock", time.Now().UnixNano()))
	if runtime.GOOS == "windows" {
		sock = fmt.Sprintf(`\\.\pipe\tailconnect-test-%d`, time.Now().UnixNano())
	}
	d := exec.Command(daemon, "--tun=userspace-networking", "--statedir="+dir, "--socket="+sock, "--port=0")
	d.Env = append(os.Environ(), "TS_NO_LOGS_NO_SUPPORT=true")
	dlog, _ := os.Create(filepath.Join(dir, "tailscaled.log"))
	d.Stdout, d.Stderr = dlog, dlog
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Process.Kill(); d.Wait(); os.Remove(sock) })
	up := exec.Command(cli, "--socket="+sock, "up", "--login-server="+url, "--hostname=alpha-ts", "--timeout=60s")
	var err error
	for i := 0; i < 50; i++ { // wait for the daemon's socket
		if out, e := up.CombinedOutput(); e == nil {
			err = nil
			break
		} else {
			err = fmt.Errorf("%v: %s", e, out)
			time.Sleep(200 * time.Millisecond)
			up = exec.Command(cli, "--socket="+sock, "up", "--login-server="+url, "--hostname=alpha-ts", "--timeout=60s")
		}
	}
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(dir, "tailscaled.log"))
		t.Fatalf("tailscale up: %v\n%s", err, b)
	}
	lc := &local.Client{Socket: sock, UseSocketOnly: true}

	a := newCoreWith(t, url, "alpha", true, lc)
	b := newCore(t, url, "bravo", true)
	dumpOnFail = []*Core{a, b}
	if m := a.Snapshot(context.Background()).Tailnet.Mode; m != "app" {
		t.Fatalf("mode = %q", m)
	}
	waitFor(t, "app-mode and embedded devices to pair", 90*time.Second, func() bool {
		da, ok1 := deviceNamed(a, "bravo")
		db, ok2 := deviceNamed(b, "alpha")
		return ok1 && ok2 && da.TrustsUs && db.TrustsUs && da.BuiltinSSH && db.BuiltinSSH
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, pair := range []struct {
		from *Core
		to   string
	}{{a, "bravo"}, {b, "alpha"}} {
		d, _ := deviceNamed(pair.from, pair.to)
		if _, err := pair.from.Stats(ctx, d.ID); err != nil {
			t.Errorf("%s -> %s stats: %v", pair.from.Name(), pair.to, err)
		}
		cl, err := sshx.Connect(ctx, pair.from.sshParams(pair.from.mustDevice(t, d.ID), ""))
		if err != nil {
			t.Errorf("%s -> %s ssh: %v", pair.from.Name(), pair.to, err)
			continue
		}
		out, err := sshx.Run(ctx, cl, "echo app-mode-ok", nil)
		cl.Close()
		if err != nil || strings.TrimSpace(out) != "app-mode-ok" {
			t.Errorf("%s -> %s ssh run: %q %v", pair.from.Name(), pair.to, out, err)
		}
	}
	// A local process connecting straight to the loopback listener can't be
	// identified and must be refused.
	resp, err := http.Get("http://127.0.0.1:4747/v1/stats")
	if err == nil {
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("unidentified local caller got %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	// Logging out must never touch the user's Tailscale.
	if err := a.Logout(ctx); err == nil {
		t.Error("logout in app mode should be refused")
	}
}

func TestRevokeStaysRevoked(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	url := startControl(t, true)
	a := newCore(t, url, "alpha", true)
	b := newCore(t, url, "bravo", true)
	dumpOnFail = []*Core{a, b}
	waitFor(t, "auto-pair", 60*time.Second, func() bool {
		d, ok := deviceNamed(b, "alpha")
		return ok && d.TrustsUs && d.TrustedByUs
	})
	aNode := a.Snapshot(context.Background()).Tailnet.NodeID
	if err := b.RevokeTrust(aNode); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second) // many discovery/auto-pair cycles
	if b.IsTrusted(aNode) {
		t.Fatal("revoked own-account device was auto-paired again")
	}
}

func TestScreenSharingFindsRustDesk(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	// Never open a real viewer on the machine running the tests.
	if ok, _ := launch.ScreenViewer(launch.ScreenRustDesk); ok {
		t.Skip("RustDesk is installed here; this test would open it")
	}
	for _, p := range []string{"5900", "3389", "21118"} {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 300*time.Millisecond); err == nil {
			c.Close()
			t.Skipf("port %s is in use here; this test would open a real viewer", p)
		}
	}
	url := startControl(t, true)
	testWindowed = true // screen sharing needs a desktop app
	a := newCore(t, url, "alpha", true)
	testWindowed = false
	b := newCore(t, url, "bravo", true)
	dumpOnFail = []*Core{a, b}
	waitFor(t, "auto-pair", 60*time.Second, func() bool {
		d, ok := deviceNamed(a, "bravo")
		return ok && d.TrustsUs && d.TrustedByUs && d.Online
	})
	devB, _ := deviceNamed(a, "bravo")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Nothing is set up on bravo: explain how, including RustDesk.
	_, err := a.RemoteDesktop(ctx, devB.ID)
	if err == nil || !strings.Contains(err.Error(), "isn't enabled") || !strings.Contains(err.Error(), "RustDesk") {
		t.Fatalf("nothing enabled: %v", err)
	}
	t.Logf("nothing enabled -> %v", err)
	// RustDesk with direct IP access on bravo (its port, through the tunnel),
	// but no RustDesk here: say exactly what's missing on this side.
	ln, err := net.Listen("tcp", "127.0.0.1:21118")
	if err != nil {
		t.Skipf("can't listen on 21118: %v", err)
	}
	defer ln.Close()
	_, err = a.RemoteDesktop(ctx, devB.ID)
	if err == nil || !strings.Contains(err.Error(), "needs a viewer") || !strings.Contains(err.Error(), "RustDesk: install RustDesk") {
		t.Fatalf("RustDesk on the other side: %v", err)
	}
	t.Logf("RustDesk there, not here -> %v", err)
}
