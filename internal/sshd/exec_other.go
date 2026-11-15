//go:build !(darwin || linux || freebsd || windows)

package sshd

import "golang.org/x/crypto/ssh"

// Windows needs ConPTY support; until then devices fall back to the system
// OpenSSH server.
const supported = false

var defaultShell = "cmd.exe"

func start(string, []string, *ptyReq, string, ssh.Channel) (*running, error) {
	return nil, ErrUnsupported
}

func serveSFTP(ssh.Channel) {}
