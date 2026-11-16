// Command relsign manages TailConnect's release signing key. The private key
// stays on the maintainer's machine (never in CI or the repository); apps
// verify SHA256SUMS against the public key compiled into them before
// installing an update.
//
//	go run ./tools/relsign keygen <private-key-file>   # once; prints the public key
//	go run ./tools/relsign encrypt <private-key-file>  # protect an older key with a passphrase
//	go run ./tools/relsign pubkey <private-key-file>   # print the public key
//	go run ./tools/relsign sign <private-key-file> <SHA256SUMS>  # writes SHA256SUMS.sig
//	go run ./tools/relsign verify <public-key> <SHA256SUMS> <SHA256SUMS.sig>
//
// New keys are encrypted with a passphrase (scrypt + XChaCha20-Poly1305), so
// a copy of the key file alone — from a backup, a synced folder or malware
// reading files — can't sign a release. The passphrase is read from the
// terminal, never from arguments or the environment.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"

	"github.com/imbikramsaha/tailconnect/internal/update"
)

const (
	plainHeader = "tailconnect-release-key-v1"
	sealHeader  = "tailconnect-release-key-v2"
	minPassLen  = 12
)

// scrypt cost; tests lower it. 2^17 takes about half a second and 128 MiB.
var scryptN, scryptR, scryptP = 1 << 17, 8, 1

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: relsign keygen <key> | encrypt <key> | pubkey <key> | sign <key> <file> | verify <pubkey> <file> <sig>")
	}
	switch args[0] {
	case "keygen":
		if _, err := os.Stat(args[1]); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite a signing key", args[1])
		}
		pass, err := newPassphrase()
		if err != nil {
			return err
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		line, err := seal(priv.Seed(), pass)
		if err != nil {
			return err
		}
		if err := writeKey(args[1], line, false); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	case "encrypt":
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(raw), sealHeader) {
			return errors.New("this key is already protected with a passphrase")
		}
		seed, err := parsePlain(raw)
		if err != nil {
			return err
		}
		pass, err := newPassphrase()
		if err != nil {
			return err
		}
		line, err := seal(seed, pass)
		if err != nil {
			return err
		}
		// Prove the new file opens to the same key before replacing the old one.
		if got, err := open([]byte(line), pass); err != nil || !bytes.Equal(got, seed) {
			return errors.New("encrypted key failed to verify; the original file is unchanged")
		}
		if err := writeKey(args[1], line, true); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Key encrypted. Delete any other unencrypted copies (backups, synced folders).")
		return nil
	case "pubkey":
		priv, err := loadKey(args[1])
		if err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return nil
	case "sign":
		if len(args) != 3 {
			return errors.New("usage: relsign sign <key> <SHA256SUMS>")
		}
		priv, err := loadKey(args[1])
		if err != nil {
			return err
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		sig := ed25519.Sign(priv, update.SignedMessage(data))
		return os.WriteFile(args[2]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
	case "verify":
		if len(args) != 4 {
			return errors.New("usage: relsign verify <pubkey> <SHA256SUMS> <SHA256SUMS.sig>")
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(args[3])
		if err != nil {
			return err
		}
		if err := update.VerifySignature([]string{args[1]}, data, sig); err != nil {
			return err
		}
		fmt.Println("signature OK")
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// loadKey reads a key file, asking for the passphrase if it is encrypted.
func loadKey(path string) (ed25519.PrivateKey, error) {
	// (Windows has no Unix permission bits; ACLs protect the user profile.)
	if fi, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users; run: chmod 600 %s", path, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var seed []byte
	if strings.HasPrefix(string(raw), sealHeader) {
		pass, err := readPassphrase("Release key passphrase: ")
		if err != nil {
			return nil, err
		}
		if seed, err = open(raw, pass); err != nil {
			return nil, err
		}
	} else {
		if seed, err = parsePlain(raw); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "warning: %s is not passphrase-protected; run: go run ./tools/relsign encrypt %s\n", path, path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func parsePlain(raw []byte) ([]byte, error) {
	f := strings.Fields(string(raw))
	if len(f) != 2 || f[0] != plainHeader {
		return nil, errors.New("not a TailConnect release key file")
	}
	seed, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("corrupt key file")
	}
	return seed, nil
}

// seal encrypts a key seed: "<header> scrypt <N> <r> <p> <salt> <nonce> <ciphertext>".
func seal(seed, pass []byte) (string, error) {
	salt := make([]byte, 16)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	aead, err := keyAEAD(pass, salt, scryptN, scryptR, scryptP)
	if err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, seed, []byte(sealHeader))
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("%s scrypt %d %d %d %s %s %s\n", sealHeader, scryptN, scryptR, scryptP, b64(salt), b64(nonce), b64(ct)), nil
}

func open(raw, pass []byte) ([]byte, error) {
	var n, r, p int
	var salt64, nonce64, ct64, kdf string
	_, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(string(raw)), sealHeader+" "), "%s %d %d %d %s %s %s", &kdf, &n, &r, &p, &salt64, &nonce64, &ct64)
	if err != nil || kdf != "scrypt" || n < 1<<14 && n != scryptN || n > 1<<22 || r < 1 || r > 32 || p < 1 || p > 16 {
		return nil, errors.New("corrupt key file")
	}
	salt, e1 := base64.StdEncoding.DecodeString(salt64)
	nonce, e2 := base64.StdEncoding.DecodeString(nonce64)
	ct, e3 := base64.StdEncoding.DecodeString(ct64)
	if e1 != nil || e2 != nil || e3 != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, errors.New("corrupt key file")
	}
	aead, err := keyAEAD(pass, salt, n, r, p)
	if err != nil {
		return nil, err
	}
	seed, err := aead.Open(nil, nonce, ct, []byte(sealHeader))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("wrong passphrase (or the key file is damaged)")
	}
	return seed, nil
}

func keyAEAD(pass, salt []byte, n, r, p int) (interface {
	Seal(dst, nonce, plaintext, ad []byte) []byte
	Open(dst, nonce, ciphertext, ad []byte) ([]byte, error)
}, error) {
	k, err := scrypt.Key(pass, salt, n, r, p, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(k)
}

// writeKey writes the key line with owner-only permissions, atomically when
// replacing an existing file.
func writeKey(path, line string, replace bool) error {
	if !replace {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(line); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".relsign-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(line); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Passphrases come from the terminal only (never arguments or environment,
// which other processes or shell history can see).
var readPassphrase = func(prompt string) ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("the release key is passphrase-protected: run this in a terminal")
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt)
	pass, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return nil, err
	}
	return pass, nil
}

func newPassphrase() ([]byte, error) {
	p1, err := readPassphrase(fmt.Sprintf("New passphrase for the release key (at least %d characters): ", minPassLen))
	if err != nil {
		return nil, err
	}
	if len(p1) < minPassLen {
		return nil, fmt.Errorf("passphrase too short (at least %d characters)", minPassLen)
	}
	p2, err := readPassphrase("Repeat the passphrase: ")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(p1, p2) {
		return nil, errors.New("passphrases don't match")
	}
	return p1, nil
}
