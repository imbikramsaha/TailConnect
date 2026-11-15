// Command relsign manages TailConnect's release signing key. The private key
// stays on the maintainer's machine (never in CI or the repository); apps
// verify SHA256SUMS against the public key compiled into them before
// installing an update.
//
//	go run ./tools/relsign keygen <private-key-file>   # once; prints the public key
//	go run ./tools/relsign sign <private-key-file> <SHA256SUMS>  # writes SHA256SUMS.sig
//	go run ./tools/relsign verify <public-key> <SHA256SUMS> <SHA256SUMS.sig>
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/imbikramsaha/tailconnect/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: relsign keygen <key> | sign <key> <file> | verify <pubkey> <file> <sig>")
	}
	switch args[0] {
	case "keygen":
		if _, err := os.Stat(args[1]); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite a signing key", args[1])
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(priv.Seed())
		if err := os.WriteFile(args[1], []byte("tailconnect-release-key-v1 "+enc+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	case "sign":
		if len(args) != 3 {
			return errors.New("usage: relsign sign <key> <SHA256SUMS>")
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		f := strings.Fields(string(raw))
		if len(f) != 2 || f[0] != "tailconnect-release-key-v1" {
			return errors.New("not a TailConnect release key file")
		}
		seed, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("corrupt key file")
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), update.SignedMessage(data))
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
