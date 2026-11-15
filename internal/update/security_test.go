package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"
)

func TestReleaseSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := []string{base64.StdEncoding.EncodeToString(pub)}
	sums := []byte("abc  TailConnect-1.0.0-macos.dmg\n")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SignedMessage(sums))))
	if err := VerifySignature(keys, sums, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if VerifySignature(keys, []byte("evil  TailConnect-1.0.0-macos.dmg\n"), sig) == nil {
		t.Error("tampered checksums accepted")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	bad := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, SignedMessage(sums))))
	if VerifySignature(keys, sums, bad) == nil {
		t.Error("signature from another key accepted")
	}
	if VerifySignature(keys, sums, []byte("not base64!")) == nil || VerifySignature(nil, sums, sig) == nil {
		t.Error("malformed signature or empty key list accepted")
	}
	// The shipped key must be a real key, not a placeholder.
	k, err := base64.StdEncoding.DecodeString(ReleaseKeys[0])
	if err != nil || len(k) != ed25519.PublicKeySize {
		t.Fatalf("ReleaseKeys[0] is not an ed25519 public key")
	}
}

func TestExactAssetNames(t *testing.T) {
	r := &Release{Version: "9.9.9", Assets: map[string]string{
		"TailConnect-0.2.1-macos.dmg": "u1", // replayed older asset
	}}
	if _, _, ok := macAsset(r); ok {
		t.Error("asset from another version accepted")
	}
	r.Assets["TailConnect-9.9.9-macos.dmg"] = "u2"
	if n, u, ok := macAsset(r); !ok || u != "u2" || n != "TailConnect-9.9.9-macos.dmg" {
		t.Errorf("got %s %s %v", n, u, ok)
	}
}

func TestVersionOrdering(t *testing.T) {
	if !Newer("1.0.0-rc.10", "1.0.0-rc.9") || Newer("1.0.0-rc.9", "1.0.0-rc.10") {
		t.Error("numeric pre-release ordering")
	}
	if Valid("nightly") || Valid("v1.2.3;rm") || !Valid("v1.2.3-beta.2") {
		t.Error("tag validation")
	}
}

func TestRedirectPolicy(t *testing.T) {
	mk := func(raw string) *http.Request { u, _ := url.Parse(raw); return &http.Request{URL: u} }
	if checkRedirect(mk("https://objects.githubusercontent.com/x"), nil) != nil {
		t.Error("GitHub asset host refused")
	}
	for _, bad := range []string{"http://objects.githubusercontent.com/x", "https://evil.example/x"} {
		if checkRedirect(mk(bad), nil) == nil {
			t.Errorf("redirect to %s allowed", bad)
		}
	}
}
