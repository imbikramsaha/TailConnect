package sshkeys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorizeRevoke(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreate(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if id2, _ := LoadOrCreate(dir, "test"); id2.PublicLine != id.PublicLine {
		t.Fatal("key not persisted")
	}
	pub, err := ValidatePublicKey(id.PublicLine + " some comment")
	if err != nil {
		t.Fatal(err)
	}
	ak := filepath.Join(dir, "authorized_keys")
	os.WriteFile(ak, []byte("ssh-ed25519 AAAAexisting user@host\n"), 0o600)
	for i := 0; i < 2; i++ { // re-authorizing replaces, not duplicates
		if err := Authorize(ak, pub, "nodeA", "Alpha Mac"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(ak)
	if strings.Count(string(b), pub) != 1 || !strings.Contains(string(b), "user@host") {
		t.Fatalf("authorized_keys:\n%s", b)
	}
	if err := Revoke(ak, "nodeA"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ak); string(b) != "ssh-ed25519 AAAAexisting user@host\n" {
		t.Fatalf("after revoke:\n%s", b)
	}
	for _, bad := range []string{"", "not a key", `command="x" ` + pub, pub + "\nssh-rsa AAAA"} {
		if _, err := ValidatePublicKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
