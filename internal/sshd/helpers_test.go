package sshd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func signer(t *testing.T) ssh.Signer {
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startSSHD(t *testing.T, allowed ssh.PublicKey) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		HostKey: signer(t),
		Auth: func(_ context.Context, _ string, key ssh.PublicKey) (string, error) {
			if bytes.Equal(key.Marshal(), allowed.Marshal()) {
				return "tester", nil
			}
			return "", errors.New("not paired")
		},
	}
	go s.Serve(ln)
	t.Cleanup(s.Close)
	return ln.Addr().String()
}

func dial(t *testing.T, addr string, k ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: "anyone", Auth: []ssh.AuthMethod{ssh.PublicKeys(k)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	})
}
