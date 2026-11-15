// Package sshkeys manages TailConnect's SSH identity, authorized_keys
// entries for trusted peers, and a trust-on-first-use known_hosts file.
package sshkeys

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/imbikramsaha/tailconnect/internal/store"
)

// Marker prefixes the comment of every authorized_keys line TailConnect adds,
// so entries can be found and removed without touching anything else.
const Marker = "tailconnect:"

// Identity is this device's SSH key pair.
type Identity struct {
	Signer     ssh.Signer
	PublicLine string // authorized_keys format, no comment
	PrivPath   string
}

// LoadOrCreate loads <dir>/id_ed25519 or generates a new ed25519 key.
func LoadOrCreate(dir, comment string) (*Identity, error) {
	priv := filepath.Join(dir, "id_ed25519")
	b, err := os.ReadFile(priv)
	if errors.Is(err, os.ErrNotExist) {
		_, key, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		blk, merr := ssh.MarshalPrivateKey(key, comment)
		if merr != nil {
			return nil, merr
		}
		b = pem.EncodeToMemory(blk)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := store.WriteFileAtomic(priv, b, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", priv, err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if err := store.WriteFileAtomic(priv+".pub", []byte(line+" "+comment+"\n"), 0o644); err != nil {
		return nil, err
	}
	return &Identity{Signer: signer, PublicLine: line, PrivPath: priv}, nil
}

var akMu sync.Mutex

// AuthorizedKeysPath returns ~/.ssh/authorized_keys.
func AuthorizedKeysPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), nil
}

// ValidatePublicKey checks that line is a single authorized_keys public key
// and returns it normalised (type + base64, no options or comment).
func ValidatePublicKey(line string) (string, error) {
	if strings.ContainsAny(line, "\r\n") {
		return "", errors.New("public key must be a single line")
	}
	pk, _, opts, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", fmt.Errorf("invalid public key: %w", err)
	}
	if len(opts) > 0 {
		return "", errors.New("public key must not carry options")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), nil
}

// sanitizeTag keeps marker tags shell/authorized_keys safe.
func sanitizeTag(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// Authorize adds pubKey to authorized_keys tagged with tag, replacing any
// previous entry with the same tag. pubKey must already be validated.
func Authorize(path, pubKey, tag, name string) error {
	akMu.Lock()
	defer akMu.Unlock()
	mark := Marker + sanitizeTag(tag)
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	out := lines[:0]
	for _, l := range lines {
		if hasMarker(l, mark) {
			continue
		}
		out = append(out, l)
	}
	out = append(out, fmt.Sprintf("%s %s:%s", pubKey, mark, sanitizeTag(name)))
	return writeLines(path, out)
}

// Revoke removes the entry tagged with tag.
func Revoke(path, tag string) error {
	akMu.Lock()
	defer akMu.Unlock()
	mark := Marker + sanitizeTag(tag)
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	out := lines[:0]
	changed := false
	for _, l := range lines {
		if hasMarker(l, mark) {
			changed = true
			continue
		}
		out = append(out, l)
	}
	if !changed {
		return nil
	}
	return writeLines(path, out)
}

func hasMarker(line, mark string) bool {
	f := strings.Fields(line)
	if len(f) < 3 {
		return false
	}
	c := f[len(f)-1]
	return c == mark || strings.HasPrefix(c, mark+":")
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func writeLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	return store.WriteFileAtomic(path, buf.Bytes(), 0o600)
}

// KnownHosts is a trust-on-first-use host key store shared with OpenSSH via
// UserKnownHostsFile in the generated ssh config.
type KnownHosts struct {
	mu   sync.Mutex
	path string
}

// NewKnownHosts returns a TOFU store backed by path.
func NewKnownHosts(path string) *KnownHosts { return &KnownHosts{path: path} }

// Path returns the known_hosts file path.
func (k *KnownHosts) Path() string { return k.path }

// Callback returns a HostKeyCallback that checks keys for hostAlias (used
// instead of the dialled address so tunnelled and direct connections agree
// with OpenSSH's HostKeyAlias). Unknown hosts are recorded; changed keys fail.
func (k *KnownHosts) Callback(hostAlias string) ssh.HostKeyCallback {
	// The dialled address varies (tunnel, LAN IP, tailnet IP); lookups are
	// keyed on the alias only, so a fixed placeholder remote is passed.
	remote := &net.TCPAddr{IP: net.IPv4zero, Port: 22}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		k.mu.Lock()
		defer k.mu.Unlock()
		if _, err := os.Stat(k.path); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(k.path, nil, 0o600); err != nil {
				return err
			}
		}
		cb, err := knownhosts.New(k.path)
		if err != nil {
			return err
		}
		addr := knownhosts.Normalize(hostAlias)
		err = cb(net.JoinHostPort(hostAlias, "22"), remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			f, ferr := os.OpenFile(k.path, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			_, ferr = fmt.Fprintln(f, knownhosts.Line([]string{addr}, key))
			return ferr
		}
		if errors.As(err, &ke) {
			return fmt.Errorf("host key for %s changed (possible impersonation); remove its line from %s if this is expected", hostAlias, k.path)
		}
		return err
	}
}

// Forget removes recorded keys for hostAlias.
func (k *KnownHosts) Forget(hostAlias string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	lines, err := readLines(k.path)
	if err != nil || lines == nil {
		return err
	}
	addr := knownhosts.Normalize(hostAlias)
	out := lines[:0]
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == addr {
			continue
		}
		out = append(out, l)
	}
	return writeLines(k.path, out)
}
