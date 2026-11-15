package sshd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Access withdrawn while connected: new sessions on the existing connection
// are refused, and Close drops it.
func TestRevokeWhileConnected(t *testing.T) {
	key := signer(t)
	var allowed atomic.Bool
	allowed.Store(true)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	s := &Server{
		HostKey: signer(t),
		Auth: func(_ context.Context, _ string, k ssh.PublicKey) (string, error) {
			if allowed.Load() && bytes.Equal(k.Marshal(), key.PublicKey().Marshal()) {
				return "tester", nil
			}
			return "", errors.New("revoked")
		},
	}
	go s.Serve(ln)
	defer s.Close()
	cl, err := dial(t, ln.Addr().String(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if sess, err := cl.NewSession(); err != nil {
		t.Fatal(err)
	} else {
		sess.Close()
	}
	allowed.Store(false)
	if sess, err := cl.NewSession(); err == nil {
		sess.Close()
		t.Fatal("new session allowed after access was withdrawn")
	}
	// Admit: unpaired callers are dropped before the handshake.
	s2 := &Server{HostKey: signer(t), Auth: s.Auth, Admit: func(context.Context, string) bool { return false }}
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	go s2.Serve(ln2)
	defer s2.Close()
	allowed.Store(true)
	if _, err := dial(t, ln2.Addr().String(), key); err == nil {
		t.Fatal("connection not admitted still completed a handshake")
	}
}
