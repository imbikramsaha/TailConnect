package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}

type fakeID struct{}

func (fakeID) Identify(context.Context, string) (Caller, error) {
	return Caller{NodeID: "n-client", Name: "client"}, nil
}

// fakeHost implements what the hello handler uses; anything else panics.
type fakeHost struct {
	Host
	deviceKey string
}

func (fakeHost) Access(string) string  { return "" }
func (fakeHost) PeerKey(string) string { return "" }
func (h fakeHost) Hello(Caller, string) Hello {
	return Hello{App: AppID, Name: "server", DeviceKey: h.deviceKey}
}

func serveHello(t *testing.T, signer ssh.Signer, key string) *Client {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{ID: fakeID{}, Host: fakeHost{deviceKey: key}, Signer: signer}
	go s.Serve(ln)
	t.Cleanup(func() { s.Close() })
	clientSigner, _ := testSigner(t)
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	}
	return NewClient(dial, clientSigner)
}

func TestHelloAnswersAreSigned(t *testing.T) {
	serverSigner, serverKey := testSigner(t)
	_, otherKey := testSigner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, proof, err := serveHello(t, serverSigner, serverKey).Hello(ctx, "100.64.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if h.DeviceKey != serverKey || !proof(serverKey) {
		t.Fatal("answer not verifiable with the device's key")
	}
	if proof(otherKey) {
		t.Fatal("answer verified with an unrelated key")
	}
	// An older TailConnect (or an impostor) that doesn't sign.
	_, proof, err = serveHello(t, nil, serverKey).Hello(ctx, "100.64.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if proof(serverKey) {
		t.Fatal("unsigned answer verified")
	}
}

func TestResponseSignatureBinding(t *testing.T) {
	signer, key := testSigner(t)
	nonce := strings.Repeat("ab", 16)
	body := []byte(`{"app":"tailconnect"}`)
	sig, err := signResponse(signer, nonce, body)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyResponse(key, nonce, body, sig) {
		t.Fatal("valid signature rejected")
	}
	if verifyResponse(key, strings.Repeat("cd", 16), body, sig) {
		t.Fatal("signature replayed for another request")
	}
	if verifyResponse(key, nonce, []byte(`{"app":"tailconnect","x":1}`), sig) {
		t.Fatal("tampered body accepted")
	}
}
