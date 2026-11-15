package sshd

import (
	"errors"
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

func TestBuiltinSSHWindows(t *testing.T) {
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

	sess, _ := cl.NewSession()
	out, err := sess.CombinedOutput("echo hello")
	if err != nil || !strings.Contains(string(out), "hello") {
		t.Fatalf("exec: %q %v", out, err)
	}
	sess, _ = cl.NewSession()
	err = sess.Run("exit 3")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Fatalf("exit status: %v", err)
	}
	sess, _ = cl.NewSession()
	sess.Stdin = strings.NewReader("piped-data\r\n")
	if out, err := sess.Output(`findstr "^"`); err != nil || !strings.Contains(string(out), "piped-data") {
		t.Fatalf("stdin: %q %v", out, err)
	}

	// ConPTY shell running a command.
	sess, _ = cl.NewSession()
	if err := sess.RequestPty("xterm", 30, 100, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	sess.Stdout = &buf
	if err := sess.Run("echo conpty-ok"); err != nil {
		t.Fatalf("pty run: %v (%q)", err, buf.String())
	}
	if !strings.Contains(buf.String(), "conpty-ok") {
		t.Fatalf("pty output: %q", buf.String())
	}

	// SFTP with Windows paths.
	sc, err := sftp.NewClient(cl)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "f.txt")
	f, err := sc.Create("/" + filepath.ToSlash(p))
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(f, "via sftp")
	f.Close()
	sc.Close()
	if b, _ := os.ReadFile(p); string(b) != "via sftp" {
		t.Fatalf("sftp wrote %q", b)
	}

	// Loopback forwarding.
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
}
