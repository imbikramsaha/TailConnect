package forward

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestStartOnceServesOneConnection(t *testing.T) {
	// A fake remote service that echoes one line.
	srv, _ := net.Listen("tcp", "127.0.0.1:0")
	defer srv.Close()
	go func() {
		for {
			c, err := srv.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Addr().String())
	}
	port, err := StartOnce(5*time.Second, dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("relay: %q %v", buf, err)
	}
	c.Close()
	// The port no longer accepts anyone.
	time.Sleep(100 * time.Millisecond)
	if c2, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c2.Close()
		t.Fatal("second connection accepted")
	}
}

func TestStartOnceExpires(t *testing.T) {
	port, err := StartOnce(200*time.Millisecond, func(context.Context) (net.Conn, error) { return nil, io.EOF }, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
		c.Close()
		t.Fatal("unused port still listening")
	}
}
