package tailnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"tailscale.com/client/local"
)

// Modes.
const (
	ModeEmbedded = "embedded"
	ModeApp      = "app"
)

// DetectSystem returns a client for a running, signed-in Tailscale on this
// computer (the Tailscale app or tailscaled), or nil.
func DetectSystem(ctx context.Context) *local.Client {
	lc := &local.Client{}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, err := lc.StatusWithoutPeers(ctx)
	if err != nil || st.BackendState != StateRunning || st.Self == nil {
		return nil
	}
	return lc
}

// dialSystem dials through the system Tailscale: directly when it runs a
// TUN device (fastest), else through its local API (userspace networking).
func (n *Node) dialSystem(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}
	if n.systemTUN(ctx) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return n.lc.UserDial(ctx, network, host, uint16(port))
}

func (n *Node) systemTUN(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := n.lc.StatusWithoutPeers(ctx)
	return err == nil && st.TUN
}

// listenSystem listens for tailnet connections arriving through the system
// Tailscale. With a TUN device they arrive on its Tailscale IPs; in
// userspace mode tailscaled forwards them to localhost and WhoIs still
// identifies the peer (local processes connecting directly can't be
// identified, so the agent refuses them).
func (n *Node) listenSystem(network, addr string) (net.Listener, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		return nil, err
	}
	var hosts []netip.Addr
	if st.TUN {
		hosts = st.TailscaleIPs
	} else {
		hosts = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}
	}
	if len(hosts) == 0 {
		return nil, errors.New("the Tailscale app has no addresses yet")
	}
	var lns []net.Listener
	for _, h := range hosts {
		ln, err := net.Listen(network, net.JoinHostPort(h.String(), portStr))
		if err != nil {
			continue // e.g. no IPv6 loopback
		}
		lns = append(lns, ln)
	}
	if len(lns) == 0 {
		return nil, errors.New("couldn't listen on the Tailscale app's addresses (is port " + portStr + " in use?)")
	}
	if len(lns) == 1 {
		return lns[0], nil
	}
	return newMultiListener(lns), nil
}

// multiListener merges several listeners into one.
type multiListener struct {
	lns   []net.Listener
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newMultiListener(lns []net.Listener) *multiListener {
	m := &multiListener{lns: lns, conns: make(chan net.Conn), done: make(chan struct{})}
	for _, ln := range lns {
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				select {
				case m.conns <- c:
				case <-m.done:
					c.Close()
					return
				}
			}
		}(ln)
	}
	return m
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Close() error {
	m.once.Do(func() {
		close(m.done)
		for _, ln := range m.lns {
			ln.Close()
		}
	})
	return nil
}

func (m *multiListener) Addr() net.Addr { return m.lns[0].Addr() }
