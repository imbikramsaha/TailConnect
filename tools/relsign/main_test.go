package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	scryptN = 1 << 10 // fast for tests
	seed := make([]byte, ed25519.SeedSize)
	rand.Read(seed)
	line, err := seal(seed, []byte("correct horse battery"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, base64.StdEncoding.EncodeToString(seed)) {
		t.Fatal("seed stored in the clear")
	}
	got, err := open([]byte(line), []byte("correct horse battery"))
	if err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("open: %v", err)
	}
	if _, err := open([]byte(line), []byte("wrong passphrase!!")); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	// Tampering with any field is detected.
	f := strings.Fields(line)
	f[len(f)-1] = base64.StdEncoding.EncodeToString(append([]byte{1}, []byte(f[len(f)-1])[:10]...))
	if _, err := open([]byte(strings.Join(f, " ")), []byte("correct horse battery")); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestEncryptExistingKeyAndSign(t *testing.T) {
	scryptN = 1 << 10
	dir := t.TempDir()
	key := filepath.Join(dir, "release.key")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	os.WriteFile(key, []byte(plainHeader+" "+base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600)
	readPassphrase = func(string) ([]byte, error) { return []byte("a long test passphrase"), nil }
	if err := run([]string{"encrypt", key}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(key)
	if !strings.HasPrefix(string(raw), sealHeader) {
		t.Fatal("key not encrypted")
	}
	if fi, _ := os.Stat(key); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	os.WriteFile(sums, []byte("abc  file\n"), 0o644)
	if err := run([]string{"sign", key, sums}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", base64.StdEncoding.EncodeToString(pub), sums, sums + ".sig"}); err != nil {
		t.Fatal(err)
	}
	// Refuses a key file other users can read.
	if runtime.GOOS != "windows" {
		os.Chmod(key, 0o644)
		if err := run([]string{"sign", key, sums}); err == nil {
			t.Fatal("signed with a world-readable key")
		}
	}
}
