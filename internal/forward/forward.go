// Package forward exposes a remote port on localhost: every local connection
// is relayed through a dial function (agent tunnel or SSH channel).
package forward

import (
	"context"
	"net"
	"strconv"
	"sync"

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
	if ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port))); err == nil {
		f.lns = append(f.lns, ln6)
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
