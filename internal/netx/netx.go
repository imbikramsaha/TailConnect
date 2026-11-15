// Package netx holds small networking helpers shared by tunnels and forwards.
package netx

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

type closeWriter interface{ CloseWrite() error }

// Pipe copies data both ways until both directions finish, half-closing
// where supported, then closes both ends.
func Pipe(a, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	cp := func(dst, src io.ReadWriteCloser) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// BufConn is a net.Conn whose first reads drain a bufio.Reader (bytes that
// were buffered while parsing an HTTP upgrade handshake).
type BufConn struct {
	net.Conn
	R *bufio.Reader
}

func (c *BufConn) Read(p []byte) (int, error) { return c.R.Read(p) }

// CloseWrite half-closes the underlying connection when supported.
func (c *BufConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// DialLoopback connects to a port on this machine, trying IPv4 then IPv6.
func DialLoopback(ctx context.Context, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return c, nil
	}
	if c6, err6 := d.DialContext(ctx, "tcp", net.JoinHostPort("::1", strconv.Itoa(port))); err6 == nil {
		return c6, nil
	}
	return nil, err
}

// PortOpen reports whether something accepts TCP connections on a local port.
func PortOpen(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	c, err := DialLoopback(ctx, port)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// ValidPort reports whether p is a usable TCP port.
func ValidPort(p int) bool { return p > 0 && p < 65536 }

// ErrClosed reports whether err is a closed-network error.
func ErrClosed(err error) bool { return errors.Is(err, net.ErrClosed) }
