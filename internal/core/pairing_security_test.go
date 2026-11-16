package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

func testKey(t *testing.T) string {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// pairingCore is a core that knows its own identity (user 7) without a tailnet.
func pairingCore(t *testing.T) *Core {
	c := newCore(t, "", "self", true)
	c.mu.Lock()
	c.selfUserID, c.selfNodeID = 7, "self-node"
	c.mu.Unlock()
	return c
}

var ownLaptop = agent.Caller{NodeID: "n-laptop", Name: "laptop", LoginName: "me@example.com", UserID: 7}

func TestOwnDeviceKeyChangeNeedsApproval(t *testing.T) {
	c := pairingCore(t)
	ctx := context.Background()
	k1, k2 := testKey(t), testKey(t)
	if r := c.HandlePair(ctx, ownLaptop, agent.PairRequest{Name: "laptop", PublicKey: k1}); r.Status != agent.PairApproved {
		t.Fatalf("own device not auto-paired: %+v", r)
	}
	// Another program/user on the laptop (same Tailscale identity) offers its own key.
	r := c.HandlePair(ctx, ownLaptop, agent.PairRequest{Name: "laptop", PublicKey: k2})
	if r.Status != agent.PairPending {
		t.Fatalf("new key for a paired own device was not held for approval: %+v", r)
	}
	if got := c.PeerKey(ownLaptop.NodeID); got != k1 {
		t.Fatalf("paired key was replaced: %q", got)
	}
}

func TestUnpairCannotRaiseRestrictedAccess(t *testing.T) {
	c := pairingCore(t)
	ctx := context.Background()
	k := testKey(t)
	c.HandlePair(ctx, ownLaptop, agent.PairRequest{Name: "laptop", PublicKey: k})
	if err := c.SetAccess(ownLaptop.NodeID, store.AccessStats, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Unpair(ownLaptop); err != nil {
		t.Fatal(err)
	}
	r := c.HandlePair(ctx, ownLaptop, agent.PairRequest{Name: "laptop", PublicKey: k})
	if r.Status == agent.PairApproved || c.Access(ownLaptop.NodeID) != "" {
		t.Fatalf("restricted device regained access by unpairing: %+v, access %q", r, c.Access(ownLaptop.NodeID))
	}
}

func TestPendingRequestIsBoundToItsKey(t *testing.T) {
	c := pairingCore(t)
	ctx := context.Background()
	stranger := agent.Caller{NodeID: "n-other", Name: "other", LoginName: "x@example.com", UserID: 9}
	k1, k2 := testKey(t), testKey(t)
	r1 := c.HandlePair(ctx, stranger, agent.PairRequest{Name: "other", PublicKey: k1})
	if r1.Status != agent.PairPending || r1.ID == "" {
		t.Fatalf("stranger: %+v", r1)
	}
	// Same key again: same request, same code.
	if again := c.HandlePair(ctx, stranger, agent.PairRequest{Name: "other", PublicKey: k1}); again.ID != r1.ID || again.Code != r1.Code {
		t.Fatalf("repeat request changed: %+v vs %+v", again, r1)
	}
	// A different key replaces the request: the old ID can't approve the new key.
	r2 := c.HandlePair(ctx, stranger, agent.PairRequest{Name: "other", PublicKey: k2})
	if r2.ID == r1.ID {
		t.Fatal("key swapped under an existing request")
	}
	if err := c.ApprovePair(r1.ID, true, store.AccessStats); err == nil {
		t.Fatal("approved a withdrawn request")
	}
	if err := c.ApprovePair(r2.ID, true, store.AccessStats); err != nil {
		t.Fatal(err)
	}
	if c.PeerKey(stranger.NodeID) != k2 {
		t.Fatal("approval didn't apply to the key it was shown for")
	}
}

func TestPairCodesAreUnpredictable(t *testing.T) {
	c := pairingCore(t)
	ctx := context.Background()
	k := testKey(t)
	codes := map[string]bool{}
	for i := 0; i < 4; i++ {
		caller := agent.Caller{NodeID: "n-x", Name: "x", UserID: 9}
		r := c.HandlePair(ctx, caller, agent.PairRequest{Name: "x", PublicKey: k})
		codes[r.Code] = true
		_ = c.store.Update(func(s *store.State) error { s.Pending = nil; return nil })
	}
	if len(codes) < 2 {
		t.Fatal("the same key always gets the same code; it could be precomputed")
	}
}

func TestOnlyEd25519DeviceKeys(t *testing.T) {
	c := pairingCore(t)
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(&rk.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	if r := c.HandlePair(context.Background(), ownLaptop, agent.PairRequest{Name: "laptop", PublicKey: line}); r.Status != agent.PairDenied || !strings.Contains(r.Message, "ed25519") {
		t.Fatalf("non-ed25519 key: %+v", r)
	}
}
