package sshd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

const supported = true

var defaultShell = `C:\Windows\System32\cmd.exe`

// interactiveShell is used for shells (with or without a PTY); commands run
// through cmd.exe like Windows OpenSSH's default shell, which tools such
// as VS Code Remote-SSH expect.
func interactiveShell() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ps := filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`)
	if _, err := os.Stat(ps); err == nil {
		return `"` + ps + `" -NoLogo`
	}
	return `"` + cmdExe() + `"`
}

func cmdExe() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return defaultShell
}

// commandLine is the raw Windows command line for a session.
func commandLine(command string) string {
	if command == "" {
		return interactiveShell()
	}
	return `"` + cmdExe() + `" /c ` + command
}

func sessionEnv(remote string, extra []string) []string {
	env := os.Environ()
	if host, port, err := net.SplitHostPort(remote); err == nil {
		env = append(env, "SSH_CLIENT="+host+" "+port+" 4748")
	}
	return append(env, extra...)
}

// envBlock builds a CreateProcess environment block.
func envBlock(env []string) *uint16 {
	var b []uint16
	for _, kv := range env {
		b = append(b, utf16.Encode([]rune(kv))...)
		b = append(b, 0)
	}
	b = append(b, 0)
	return &b[0]
}

func start(command string, env []string, pr *ptyReq, remote string, ch ssh.Channel) (*running, error) {
	home, _ := os.UserHomeDir()
	if pr != nil {
		return startConPTY(commandLine(command), sessionEnv(remote, env), home, pr, ch)
	}
	cmd := exec.Command(cmdExe())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       commandLine(command),
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	cmd.Dir = home
	cmd.Env = sessionEnv(remote, env)
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
	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	return &running{
		resize: func(uint32, uint32) {},
		signal: func(name string) {
			if name == "KILL" || name == "TERM" || name == "INT" || name == "HUP" {
				kill()
			}
		},
		wait: func() int {
			err := cmd.Wait()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			if err != nil {
				return 255
			}
			return 0
		},
		hangup: kill,
	}, nil
}

func coord(cols, rows uint32) windows.Coord {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return windows.Coord{X: int16(cols), Y: int16(rows)}
}

// startConPTY runs cmdline attached to a Windows pseudo console.
func startConPTY(cmdline string, env []string, dir string, pr *ptyReq, ch ssh.Channel) (*running, error) {
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, err
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(coord(pr.Cols, pr.Rows), inR, outW, 0, &hpc); err != nil {
		for _, h := range []windows.Handle{inR, inW, outR, outW} {
			windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("ConPTY: %w", err)
	}
	// The pseudo console owns these ends now.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}
	defer attrs.Delete()
	// The attribute value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}
	si := new(windows.StartupInfoEx)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.ProcThreadAttributeList = attrs.List()
	// Without this a child of a process whose standard handles are
	// redirected (services, headless mode, tests) writes to those handles
	// instead of the pseudo console. Null handles + STARTF_USESTDHANDLES make
	// it use the console it's attached to.
	si.Flags |= windows.STARTF_USESTDHANDLES
	var pi windows.ProcessInformation
	cl, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}
	var dirp *uint16
	if dir != "" {
		dirp, _ = windows.UTF16PtrFromString(dir)
	}
	err = windows.CreateProcess(nil, cl, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		envBlock(env), dirp, &si.StartupInfo, &pi)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("starting shell: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	in := os.NewFile(uintptr(inW), "conpty-in")
	out := os.NewFile(uintptr(outR), "conpty-out")
	go func() { _, _ = io.Copy(in, ch) }()
	outDone := make(chan struct{})
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	go func() {
		_, _ = io.Copy(&stampWriter{w: ch, last: &lastOut}, out)
		close(outDone)
	}()
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			// ClosePseudoConsole flushes and ends the output pipe; it can
			// block until output is drained, which the copier does.
			windows.ClosePseudoConsole(hpc)
			in.Close()
		})
	}
	return &running{
		resize: func(c, r uint32) { _ = windows.ResizePseudoConsole(hpc, coord(c, r)) },
		signal: func(name string) {
			switch name {
			case "INT":
				_, _ = in.Write([]byte{3})
			case "KILL", "TERM", "HUP":
				_ = windows.TerminateProcess(pi.Process, 1)
			}
		},
		wait: func() int {
			_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
			var code uint32
			_ = windows.GetExitCodeProcess(pi.Process, &code)
			windows.CloseHandle(pi.Process)
			// ConPTY may still hold output from the exited process; closing
			// the console too early drops it. Wait until output goes quiet.
			lastOut.Store(time.Now().UnixNano()) // quiet period starts at exit
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if time.Since(time.Unix(0, lastOut.Load())) > 200*time.Millisecond {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			go closeAll()
			select {
			case <-outDone:
			case <-time.After(2 * time.Second):
			}
			out.Close()
			return int(code)
		},
		hangup: func() {
			_ = windows.TerminateProcess(pi.Process, 1)
			go closeAll()
		},
	}, nil
}

// stampWriter records when output was last written.
type stampWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (s *stampWriter) Write(p []byte) (int, error) {
	s.last.Store(time.Now().UnixNano())
	return s.w.Write(p)
}

// keepOpen stops the SFTP server from closing the channel itself, so the
// session can still send its exit status.
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
