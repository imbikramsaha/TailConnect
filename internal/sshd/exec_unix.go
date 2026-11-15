//go:build darwin || linux || freebsd

package sshd

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const supported = true

var defaultShell = "/bin/sh"

func init() {
	if _, err := os.Stat("/bin/zsh"); err == nil && strings.Contains(os.Getenv("SHELL"), "zsh") {
		defaultShell = "/bin/zsh"
	} else if _, err := os.Stat("/bin/bash"); err == nil {
		defaultShell = "/bin/bash"
	}
}

// baseEnv is a clean, sshd-like environment for sessions.
func baseEnv(remote string) []string {
	home, _ := os.UserHomeDir()
	u, _ := user.Current()
	name := ""
	if u != nil {
		name = u.Username
	}
	path := "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if home != "" {
		path = filepath.Join(home, ".local", "bin") + ":" + path
	}
	env := []string{
		"HOME=" + home, "USER=" + name, "LOGNAME=" + name, "SHELL=" + loginShell(),
		"PATH=" + path, "LANG=" + firstNonEmpty(os.Getenv("LANG"), "en_US.UTF-8"),
	}
	if t := os.Getenv("TMPDIR"); t != "" {
		env = append(env, "TMPDIR="+t)
	}
	if host, port, err := net.SplitHostPort(remote); err == nil {
		env = append(env, "SSH_CLIENT="+host+" "+port+" 4748", "SSH_CONNECTION="+host+" "+port+" 0.0.0.0 4748")
	}
	return env
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var signals = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT, "KILL": syscall.SIGKILL,
	"TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
}

func start(command string, env []string, pr *ptyReq, remote string, ch ssh.Channel) (*running, error) {
	shell := loginShell()
	home, _ := os.UserHomeDir()
	cmd := &exec.Cmd{Path: shell, Dir: home}
	if command == "" {
		cmd.Args = []string{"-" + filepath.Base(shell)} // login shell, like sshd
	} else {
		cmd.Args = []string{filepath.Base(shell), "-c", command}
	}
	cmd.Env = append(baseEnv(remote), env...)

	exitCode := func(err error) int {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ee.ExitCode()
		}
		if err != nil {
			return 255
		}
		return 0
	}
	signal := func(name string) {
		if sg, ok := signals[name]; ok && cmd.Process != nil {
			// Signal the whole process group (setsid made the child a leader).
			_ = syscall.Kill(-cmd.Process.Pid, sg)
		}
	}

	if pr != nil {
		term := pr.Term
		if term == "" {
			term = "xterm-256color"
		}
		cmd.Env = append(cmd.Env, "TERM="+term)
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(pr.Cols), Rows: uint16(pr.Rows)})
		if err != nil {
			return nil, err
		}
		go func() { _, _ = io.Copy(ptmx, ch) }()
		outDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(ch, ptmx)
			close(outDone)
		}()
		var once sync.Once
		closePTY := func() { once.Do(func() { ptmx.Close() }) }
		return &running{
			resize: func(c, r uint32) { _ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c), Rows: uint16(r)}) },
			signal: signal,
			wait: func() int {
				err := cmd.Wait()
				// Drain remaining output; background jobs may keep the PTY open.
				select {
				case <-outDone:
				case <-time.After(time.Second):
				}
				closePTY()
				return exitCode(err)
			},
			hangup: func() { signal("HUP"); closePTY() },
		}, nil
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		_, _ = io.Copy(stdin, ch)
		stdin.Close()
	}()
	return &running{
		resize: func(uint32, uint32) {},
		signal: signal,
		wait:   func() int { return exitCode(cmd.Wait()) },
		hangup: func() { signal("HUP") },
	}, nil
}

// keepOpen stops the SFTP server from closing the channel itself, so the
// session can still send its exit status (clients treat a missing status
// as failure — scp exits 1).
type keepOpen struct{ ssh.Channel }

func (keepOpen) Close() error { return nil }

func serveSFTP(ch ssh.Channel) {
	home, _ := os.UserHomeDir()
	srv, err := sftp.NewServer(keepOpen{ch}, sftp.WithServerWorkingDirectory(home))
	if err != nil {
		return
	}
	_ = srv.Serve()
	srv.Close()
}
