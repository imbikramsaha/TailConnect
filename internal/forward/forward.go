// Package forward exposes a remote port on localhost: every local connection
// is relayed through a dial function (agent tunnel or SSH channel).
package forward

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/netx"
)

// DialFunc opens a stream to the remote port.
type DialFunc func(ctx context.Context) (net.Conn, error)

// Forward is a running local listener.
type Forward struct {
	Port   int
	lns    []net.Listener
	dial   DialFunc
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	Logf   func(string, ...any)
}

// Start listens on 127.0.0.1:preferred, or on a free port if preferred is
// taken (local port collisions are resolved automatically).
func Start(preferred int, dial DialFunc, logf func(string, ...any)) (*Forward, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(preferred)))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	f := &Forward{Port: port, lns: []net.Listener{ln}, dial: dial, ctx: ctx, cancel: cancel, Logf: logf}
	// Also serve ::1 so "localhost" works whichever family the client picks.
	// If someone else holds [::1]:port, browsers resolving localhost to ::1
	// would talk to them: move both listeners to a fresh port instead.
	if ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port))); err == nil {
		f.lns = append(f.lns, ln6)
	} else if ipv6Loopback() {
		ln.Close()
		cancel()
		for i := 0; i < 20; i++ {
			l4, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			p := l4.Addr().(*net.TCPAddr).Port
			if l6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(p))); err == nil {
				ctx, cancel = context.WithCancel(context.Background())
				f = &Forward{Port: p, lns: []net.Listener{l4, l6}, dial: dial, ctx: ctx, cancel: cancel, Logf: logf}
				break
			}
			l4.Close()
		}
		if len(f.lns) < 2 {
			return nil, errors.New("couldn't find a free local port")
		}
	}
	for _, l := range f.lns {
		f.wg.Add(1)
		go f.serve(l)
	}
	return f, nil
}

func (f *Forward) serve(ln net.Listener) {
	defer f.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			up, err := f.dial(f.ctx)
			if err != nil {
				if f.Logf != nil {
					f.Logf("forward :%d: %v", f.Port, err)
				}
				c.Close()
				return
			}
			netx.Pipe(c, up)
		}()
	}
}

// Close stops listening. Established streams finish on their own.
func (f *Forward) Close() {
	f.cancel()
	for _, l := range f.lns {
		l.Close()
	}
	f.wg.Wait()
}

// ipv6Loopback reports whether ::1 is usable on this machine.
func ipv6Loopback() bool {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// StartOnce listens on a random 127.0.0.1 port for exactly one connection,
// relays it through dial, and stops listening — so a port opened for one
// viewer (e.g. a RustDesk session) can't be reused by anyone else on this
// computer afterwards. It also stops if nothing connects within wait.
func StartOnce(wait time.Duration, dial DialFunc, logf func(string, ...any)) (port int, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port = ln.Addr().(*net.TCPAddr).Port
	timer := time.AfterFunc(wait, func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		timer.Stop()
		ln.Close()
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		up, err := dial(ctx)
		cancel()
		if err != nil {
			if logf != nil {
				logf("forward once :%d: %v", port, err)
			}
			c.Close()
			return
		}
		netx.Pipe(c, up)
	}()
	return port, nil
}
