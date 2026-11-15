package core

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/imbikramsaha/tailconnect/internal/logx"
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

func newCore(t *testing.T, controlURL, name string, autoTrust bool) *Core {
	t.Helper()
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
		},
		Exe:               "tailconnect",
		SSHDir:            filepath.Join(dir, "ssh"),
		AuthorizedKeys:    filepath.Join(dir, "authorized_keys"),
		OpenURL:           func(string) error { return nil },
		DiscoveryInterval: 250 * time.Millisecond,
		ProbeTailConnect:  time.Second,
		ProbeOther:        time.Second,
		Headless:          true,
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

	// Connect.
	if err := a.Connect(devB.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", 30*time.Second, func() bool {
		d, _ := deviceNamed(a, "bravo")
		return d.State == StateConnected
	})

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
	if d, _ := deviceNamed(a, "bravo"); d.Pairing != "waiting" && d.Pairing != "requesting" {
		t.Errorf("alpha pairing state = %q, want waiting", d.Pairing)
	}
	if err := b.ApprovePair(reqID, true); err != nil {
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
	if err := a.ApprovePair(reqID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bravo to save alpha", 20*time.Second, func() bool {
		d, ok := deviceNamed(b, "alpha")
		return ok && d.Section == SectionMine && d.TrustsUs
	})
}
