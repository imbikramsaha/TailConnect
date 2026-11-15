// Package sshx is TailConnect's SSH client: one-time password setup that
// installs our key, key-based sessions, remote commands and port dialling.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
)

// Params describe how to reach and authenticate to an SSH server.
type Params struct {
	Alias      string // host key alias (matches the generated ssh config)
	User       string
	Signer     ssh.Signer
	Password   string // only used during setup; never stored
	KnownHosts *sshkeys.KnownHosts
	Dial       func(ctx context.Context) (net.Conn, error)
}

// Connect dials and authenticates.
func Connect(ctx context.Context, p Params) (*ssh.Client, error) {
	if p.User == "" {
		return nil, errors.New("username required")
	}
	conn, err := p.Dial(ctx)
	if err != nil {
		return nil, err
	}
	var auth []ssh.AuthMethod
	if p.Signer != nil {
		auth = append(auth, ssh.PublicKeys(p.Signer))
	}
	if p.Password != "" {
		pw := p.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(
			func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}))
	}
	cfg := &ssh.ClientConfig{
		User:            p.User,
		Auth:            auth,
		HostKeyCallback: p.KnownHosts.Callback(p.Alias),
		Timeout:         15 * time.Second,
	}
	dl := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = conn.SetDeadline(dl)
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(p.Alias, "22"), cfg)
	if err != nil {
		conn.Close()
		return nil, friendly(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

func friendly(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("authentication failed (check username/password): %w", err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("SSH server not reachable: %w", err)
	}
	return err
}

// Run executes cmd and returns stdout. stderr is included in errors.
func Run(ctx context.Context, c *ssh.Client, cmd string, stdin io.Reader) (string, error) {
	sess, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if stdin != nil {
		sess.Stdin = stdin
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case err := <-done:
		if err != nil {
			e := strings.TrimSpace(errb.String())
			if e != "" {
				return out.String(), fmt.Errorf("%w: %s", err, e)
			}
			return out.String(), err
		}
		return out.String(), nil
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		sess.Close()
		return out.String(), ctx.Err()
	}
}

// InstallKey appends pubLine (validated authorized_keys key) with comment to
// the remote ~/.ssh/authorized_keys if not already present. POSIX hosts only.
func InstallKey(ctx context.Context, c *ssh.Client, pubLine, comment string) error {
	if strings.ContainsAny(pubLine+comment, "'\n\r") {
		return errors.New("refusing to install key with unsafe characters")
	}
	f := strings.Fields(pubLine)
	if len(f) < 2 {
		return errors.New("bad public key")
	}
	cmd := fmt.Sprintf(`umask 077; mkdir -p "$HOME/.ssh" && touch "$HOME/.ssh/authorized_keys" && `+
		`(grep -qF '%s' "$HOME/.ssh/authorized_keys" || printf '%%s\n' '%s %s' >> "$HOME/.ssh/authorized_keys")`,
		f[1], pubLine, comment)
	if _, err := Run(ctx, c, cmd, nil); err != nil {
		return fmt.Errorf("installing SSH key (automatic setup supports Linux/macOS hosts): %w", err)
	}
	return nil
}

// HomeDir returns the remote user's home directory.
func HomeDir(ctx context.Context, c *ssh.Client) string {
	out, err := Run(ctx, c, `printf %s "$HOME"`, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Keepalive pings c every interval until ctx ends; returns the first error.
func Keepalive(ctx context.Context, c *ssh.Client, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			errc := make(chan error, 1)
			go func() {
				_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
				errc <- err
			}()
			select {
			case err := <-errc:
				if err != nil {
					return err
				}
			case <-time.After(interval):
				return errors.New("ssh keepalive timed out")
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// ShellQuote single-quotes s for POSIX shells.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
