//go:build darwin || linux

package sshd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestBuiltinSSH(t *testing.T) {
	key := signer(t)
	addr := startSSHD(t, key.PublicKey())

	if _, err := dial(t, addr, signer(t)); err == nil {
		t.Fatal("unpaired key was accepted")
	}
	cl, err := dial(t, addr, key)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	// exec + exit status
	sess, _ := cl.NewSession()
	out, err := sess.CombinedOutput("echo hello; echo oops >&2")
	if err != nil || !strings.Contains(string(out), "hello") || !strings.Contains(string(out), "oops") {
		t.Fatalf("exec: %q %v", out, err)
	}
	sess, _ = cl.NewSession()
	err = sess.Run("exit 3")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Fatalf("exit status: %v", err)
	}
	// stdin is passed through and EOF ends the command
	sess, _ = cl.NewSession()
	sess.Stdin = strings.NewReader("piped-data")
	if out, err := sess.Output("cat"); err != nil || string(out) != "piped-data" {
		t.Fatalf("stdin: %q %v", out, err)
	}

	// PTY shell
	sess, _ = cl.NewSession()
	if err := sess.RequestPty("xterm", 40, 120, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	sess.Stdout = &buf
	if err := sess.Run("tty >/dev/null && echo is-a-tty; stty size"); err != nil {
		t.Fatalf("pty run: %v (%q)", err, buf.String())
	}
	if !strings.Contains(buf.String(), "is-a-tty") || !strings.Contains(buf.String(), "40 120") {
		t.Fatalf("pty output: %q", buf.String())
	}

	// SFTP
	sc, err := sftp.NewClient(cl)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	f, err := sc.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(f, "via sftp")
	f.Close()
	if b, _ := os.ReadFile(p); string(b) != "via sftp" {
		t.Fatalf("sftp wrote %q", b)
	}
	sc.Close()

	// Local forwarding to loopback works; other hosts are refused.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "fwd ok") })}
	go srv.Serve(ln)
	defer srv.Close()
	hc := &http.Client{Transport: &http.Transport{Dial: func(_, a string) (net.Conn, error) { return cl.Dial("tcp", a) }}}
	resp, err := hc.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "fwd ok" {
		t.Fatalf("forward body %q", b)
	}
	if c, err := cl.Dial("tcp", "1.1.1.1:80"); err == nil {
		c.Close()
		t.Fatal("forwarding to a non-loopback host was allowed")
	}
}
