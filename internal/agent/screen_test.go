package agent

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// screenHost is a paired caller with "ports" access; allow says whether it
// may reach RustDesk's port.
type screenHost struct {
	Host
	key    string
	allow  bool
	called *atomic.Int32
}

func (screenHost) Access(string) string    { return AccessPorts }
func (h screenHost) PeerKey(string) string { return h.key }
func (h screenHost) AllowPort(_ string, port int) bool {
	return h.allow && port == RustDeskPort
}
func (h screenHost) ScreenInfo() ScreenInfo {
	h.called.Add(1)
	return ScreenInfo{RustDesk: &RustDeskInfo{Port: RustDeskPort, Password: "secret"}}
}

// Asking how to view the screen starts RustDesk on the other computer, so
// a caller whose ports are limited (not including RustDesk's) must not
// reach ScreenInfo at all.
func TestScreenInfoOnlyForAllowedCallers(t *testing.T) {
	for _, allow := range []bool{false, true} {
		clientSigner, clientKey := testSigner(t)
		var called atomic.Int32
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{ID: fakeID{}, Host: screenHost{key: clientKey, allow: allow, called: &called}}
		go s.Serve(ln)
		dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, ln.Addr().String())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		si, err := NewClient(dial, ssh.Signer(clientSigner)).Screen(ctx, "100.64.0.9")
		cancel()
		s.Close()
		if err != nil {
			t.Fatalf("allow=%v: %v", allow, err)
		}
		gotInfo := si.RustDesk != nil && strings.EqualFold(si.RustDesk.Password, "secret")
		if allow != (called.Load() == 1) || allow != gotInfo {
			t.Errorf("allow=%v: ScreenInfo called %d times, got info %v", allow, called.Load(), gotInfo)
		}
	}
}
