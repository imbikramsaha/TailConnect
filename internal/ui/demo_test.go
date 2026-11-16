package ui

import (
	"context"
	"net"
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

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
)

// TestDemo runs two paired TailConnect instances on an in-process tailnet
// with live UI servers, for manual testing and screenshots:
//
//	TC_DEMO=/tmp/demo go test ./internal/ui -run TestDemo -timeout 30m
//
// It writes login URLs to $TC_DEMO/urls.txt and runs until $TC_DEMO/stop exists.
func TestDemo(t *testing.T) {
	out := os.Getenv("TC_DEMO")
	if out == "" {
		t.Skip("set TC_DEMO=<dir> to run the demo")
	}
	netns.SetEnabled(false)
	derp := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	ctl := &testcontrol.Server{DERPMap: derp, DNSConfig: &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: "tail-demo.ts.net", AllNodesSameUser: true, AllOnline: true}
	ctl.HTTPTestServer = httptest.NewUnstartedServer(ctl)
	ctl.HTTPTestServer.Start()
	defer ctl.HTTPTestServer.Close()

	var urls []string
	for _, name := range []string{"MacBook Air", "Mac mini"} {
		dir := t.TempDir()
		st, _ := store.Open(filepath.Join(dir, "state.json"))
		_ = st.Update(func(s *store.State) error {
			s.Settings.Hostname, s.Settings.ManageSSHConfig, s.Settings.Onboarded = name, false, true
			s.Settings.ControlURL = ctl.HTTPTestServer.URL
			return nil
		})
		c, err := core.New(core.Options{
			Version: "0.3.1", Store: st, Headless: true,
			Tailnet: tailnet.Config{Dir: filepath.Join(dir, "ts"), Hostname: name, ControlURL: ctl.HTTPTestServer.URL, Ephemeral: true, InMemory: true},
			SSHDir:  filepath.Join(dir, "ssh"), AuthorizedKeys: filepath.Join(dir, "ak"), InboxDir: filepath.Join(dir, "inbox"),
			DiscoveryInterval: 500 * time.Millisecond, ProbeTailConnect: 2 * time.Second, ProbeOther: 2 * time.Second,
			OpenURL: func(string) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		s := &Server{Core: c, Token: randomHex(16)}
		s.Bind(ln)
		go s.Serve(ln)
		defer s.Close()
		// Hand out fresh one-time links on demand.
		go func(s *Server, name string) {
			for {
				p := filepath.Join(out, "want-"+strings.ReplaceAll(name, " ", "_"))
				if b, err := os.ReadFile(p); err == nil {
					os.Remove(p)
					os.WriteFile(p+".url", []byte(s.LoginURL(strings.TrimSpace(string(b)))), 0o644)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(s, name)
		urls = append(urls, name+" "+s.LoginURL(""))
	}
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "urls.txt"), []byte(strings.Join(urls, "\n")+"\n"), 0o644)
	for {
		if _, err := os.Stat(filepath.Join(out, "stop")); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
