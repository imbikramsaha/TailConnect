// Package sshd is TailConnect's built-in SSH server. It listens only on the
// tailnet and accepts only paired devices with full access, authenticated by
// both their Tailscale identity and their TailConnect key. It runs shells and
// commands as the user running TailConnect, with PTYs, SFTP and local port
// forwarding to loopback (which VS Code Remote-SSH needs).
package sshd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/imbikramsaha/tailconnect/internal/netx"
)

// AuthFunc decides whether the peer at remoteAddr presenting key may log in.
// It returns a display name for logs.
type AuthFunc func(ctx context.Context, remoteAddr string, key ssh.PublicKey) (name string, err error)

// Server is the built-in SSH server.
type Server struct {
	HostKey ssh.Signer
	Auth    AuthFunc
	Logf    func(format string, args ...any)
	// OnSession is called when a session starts (for the access log).
	OnSession func(peer, kind string)
	// Admit, if set, is checked when a connection arrives (before the SSH
	// handshake) so unpaired devices can't even reach the handshake.
	Admit func(ctx context.Context, remoteAddr string) bool

	mu     sync.Mutex
	closed bool
	lns    []net.Listener
	raw    map[net.Conn]struct{} // every accepted connection, incl. handshakes
}

// maxChannels caps concurrent sessions/forwards per connection.
const maxChannels = 32

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Serve accepts connections on ln until it is closed.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return nil
	}
	s.lns = append(s.lns, ln)
	if s.raw == nil {
		s.raw = map[net.Conn]struct{}{}
	}
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			if netx.ErrClosed(err) {
				return nil
			}
			return err
		}
		go s.handleConn(c)
	}
}

// Close stops listening and drops all connections, including ones still in
// their handshake (which could otherwise finish after a revocation).
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, ln := range s.lns {
		ln.Close()
	}
	s.lns = nil
	for c := range s.raw {
		c.Close()
	}
}

func (s *Server) register(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.raw[c] = struct{}{}
	return true
}

func (s *Server) unregister(c net.Conn) {
	s.mu.Lock()
	delete(s.raw, c)
	s.mu.Unlock()
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-TailConnect",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name, err := s.Auth(ctx, meta.RemoteAddr().String(), key)
			if err != nil {
				return nil, err
			}
			return &ssh.Permissions{Extensions: map[string]string{
				"peer": name, "key": string(key.Marshal()),
			}}, nil
		},
		MaxAuthTries: 6,
	}
	cfg.AddHostKey(s.HostKey)
	return cfg
}

func (s *Server) handleConn(nc net.Conn) {
	if !s.register(nc) {
		nc.Close()
		return
	}
	defer func() {
		s.unregister(nc)
		nc.Close()
	}()
	remote := nc.RemoteAddr().String()
	if s.Admit != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ok := s.Admit(ctx, remote)
		cancel()
		if !ok {
			return
		}
	}
	_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.config())
	if err != nil {
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer sc.Close()
	peer := sc.Permissions.Extensions["peer"]
	key, kerr := ssh.ParsePublicKey([]byte(sc.Permissions.Extensions["key"]))
	// stillTrusted re-checks access (the auth result may have been cached
	// across a revocation, and access can change while connected).
	stillTrusted := func() bool {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed || kerr != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := s.Auth(ctx, remote, key)
		return err == nil
	}
	if !stillTrusted() {
		return
	}
	s.logf("sshd: %s connected (user %q)", peer, sc.User())

	go func() {
		for r := range reqs {
			// keepalive@openssh.com and friends: answer so clients see us alive.
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	var active atomic.Int32
	for nch := range chans {
		if !stillTrusted() {
			_ = nch.Reject(ssh.Prohibited, "access revoked")
			return
		}
		if active.Load() >= maxChannels {
			_ = nch.Reject(ssh.ResourceShortage, "too many channels")
			continue
		}
		switch nch.ChannelType() {
		case "session":
			ch, creqs, err := nch.Accept()
			if err != nil {
				continue
			}
			active.Add(1)
			go func() {
				defer active.Add(-1)
				s.session(peer, remote, ch, creqs)
			}()
		case "direct-tcpip":
			active.Add(1)
			go func() {
				defer active.Add(-1)
				s.directTCPIP(peer, nch)
			}()
		default:
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

// ---- port forwarding (ssh -L, VS Code) ----

var loopbackNames = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "ip6-localhost": true}

func (s *Server) directTCPIP(peer string, nch ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	ip := net.ParseIP(p.Host)
	if !loopbackNames[p.Host] && (ip == nil || !ip.IsLoopback()) {
		_ = nch.Reject(ssh.Prohibited, "TailConnect only forwards to localhost on this device")
		return
	}
	if p.Port == 0 || p.Port > 65535 {
		_ = nch.Reject(ssh.ConnectionFailed, "bad port")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var up net.Conn
	var err error
	if ip != nil {
		up, err = (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(p.Port))))
	} else {
		up, err = netx.DialLoopback(ctx, int(p.Port))
	}
	cancel()
	if err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		up.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	netx.Pipe(ch, up)
}

// ---- sessions ----

type ptyReq struct {
	Term          string
	Cols, Rows    uint32
	Width, Height uint32
	Modes         string
}

// allowedEnv are client environment variables passed through.
var allowedEnv = regexp.MustCompile(`^(LANG|LC_[A-Z_]+|TERM|COLORTERM|TZ|VSCODE_[A-Z0-9_]+)$`)

func (s *Server) session(peer, remote string, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var (
		env     []string
		pty     *ptyReq
		winch   = make(chan [2]uint32, 4)
		started bool
		proc    *running
	)
	for req := range reqs {
		ok := false
		switch req.Type {
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil && allowedEnv.MatchString(kv.Name) {
				env = append(env, kv.Name+"="+kv.Value)
				ok = true
			}
		case "pty-req":
			var p ptyReq
			if ssh.Unmarshal(req.Payload, &p) == nil && !started {
				pty, ok = &p, true
			}
		case "window-change":
			var w struct{ Cols, Rows, Width, Height uint32 }
			if ssh.Unmarshal(req.Payload, &w) == nil {
				select {
				case winch <- [2]uint32{w.Cols, w.Rows}:
				default:
				}
				ok = true
			}
		case "shell", "exec", "subsystem":
			if started {
				break
			}
			var command string
			if req.Type == "exec" {
				var e struct{ Command string }
				if ssh.Unmarshal(req.Payload, &e) != nil {
					break
				}
				command = e.Command
			}
			if req.Type == "subsystem" {
				var sub struct{ Name string }
				if ssh.Unmarshal(req.Payload, &sub) != nil || sub.Name != "sftp" {
					break
				}
				started, ok = true, true
				_ = req.Reply(true, nil)
				s.note(peer, "sftp")
				go func() {
					serveSFTP(ch)
					sendExit(ch, 0)
					ch.Close()
				}()
				continue
			}
			p, err := start(command, env, pty, remote, ch)
			if err != nil {
				fmt.Fprintf(ch.Stderr(), "tailconnect: %v\r\n", err)
				break
			}
			proc, started, ok = p, true, true
			kind := "shell"
			if command != "" {
				kind = "command"
			}
			s.note(peer, kind)
			_ = req.Reply(true, nil)
			go func() {
				for w := range winch {
					proc.resize(w[0], w[1])
				}
			}()
			go func() {
				code := proc.wait()
				sendExit(ch, code)
				ch.CloseWrite()
				ch.Close()
			}()
			continue
		case "signal":
			var sg struct{ Signal string }
			if ssh.Unmarshal(req.Payload, &sg) == nil && proc != nil {
				proc.signal(sg.Signal)
				ok = true
			}
		}
		if req.WantReply {
			_ = req.Reply(ok, nil)
		}
	}
	close(winch)
	if proc != nil {
		proc.hangup()
	}
}

func (s *Server) note(peer, kind string) {
	if s.OnSession != nil {
		s.OnSession(peer, kind)
	}
}

func sendExit(ch ssh.Channel, code int) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	_, _ = ch.SendRequest("exit-status", false, b)
}

// running is a started shell/command.
type running struct {
	resize func(cols, rows uint32)
	signal func(name string)
	wait   func() int
	hangup func()
}

// ErrUnsupported is returned on platforms without built-in SSH support.
var ErrUnsupported = errors.New("the built-in SSH server is not available on this platform")

// Supported reports whether the built-in server works on this OS.
func Supported() bool { return supported }

func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return defaultShell
}

var _ io.Reader = (ssh.Channel)(nil)
