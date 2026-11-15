//go:build darwin || linux

package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestOpenSSHClient checks compatibility with the system OpenSSH client,
// which editors (VS Code, Cursor) and scp/sftp use.
func TestOpenSSHClient(t *testing.T) {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh client")
	}
	dir := t.TempDir()
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(k, "")
	keyPath := filepath.Join(dir, "id")
	os.WriteFile(keyPath, pem.EncodeToMemory(blk), 0o600)
	sg, _ := ssh.NewSignerFromKey(k)
	addr := startSSHD(t, sg.PublicKey())
	host, port, _ := net.SplitHostPort(addr)
	opts := []string{"-i", keyPath, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes"}

	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		return string(out), err
	}
	out, err := run(append(append([]string{sshBin}, opts...), "-p", port, host, "echo openssh-ok; uname")...)
	if err != nil || !strings.Contains(out, "openssh-ok") {
		t.Fatalf("ssh exec: %v\n%s", err, out)
	}
	// Forced TTY (like `ssh -t host cmd`).
	out, err = run(append(append([]string{sshBin}, opts...), "-tt", "-p", port, host, "test -t 0 && echo tty-ok")...)
	if err != nil || !strings.Contains(out, "tty-ok") {
		t.Fatalf("ssh -tt: %v\n%s", err, out)
	}
	// scp (SFTP protocol on modern OpenSSH).
	src := filepath.Join(dir, "src.txt")
	os.WriteFile(src, []byte("scp-data"), 0o644)
	dst := filepath.Join(dir, "dst.txt")
	if scp, err := exec.LookPath("scp"); err == nil {
		out, err = run(append(append([]string{scp}, opts...), "-P", port, src, host+":"+dst)...)
		if b, _ := os.ReadFile(dst); err != nil || string(b) != "scp-data" {
			t.Fatalf("scp: %v %q\n%s", err, b, out)
		}
	}
	// -L forwarding, as VS Code Remote-SSH uses.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tunnel-ok")) })}
	go srv.Serve(ln)
	defer srv.Close()
	lp, _ := net.Listen("tcp", "127.0.0.1:0")
	localPort := lp.Addr().(*net.TCPAddr).Port
	lp.Close()
	fwd := exec.Command(sshBin, append(opts, "-N", "-p", port, "-L",
		strconv.Itoa(localPort)+":127.0.0.1:"+strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), host)...)
	if err := fwd.Start(); err != nil {
		t.Fatal(err)
	}
	defer fwd.Process.Kill()
	var body string
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(localPort))
		if err != nil {
			continue
		}
		b := make([]byte, 32)
		n, _ := resp.Body.Read(b)
		resp.Body.Close()
		body = string(b[:n])
		break
	}
	if body != "tunnel-ok" {
		t.Fatalf("ssh -L forward body %q", body)
	}
}
