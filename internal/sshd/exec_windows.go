package sshd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// TailConnect's own environment (e.g. TS_AUTHKEY) must not leak into
	// sessions.
	var env []string
	for _, kv := range os.Environ() {
		u := strings.ToUpper(kv)
		if strings.HasPrefix(u, "TS_") || strings.HasPrefix(u, "TAILCONNECT_") || strings.HasPrefix(u, "TC_") {
			continue
		}
		env = append(env, kv)
	}
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
		CmdLine:    commandLine(command),
		HideWindow: true,
		// Suspended until it is in the kill-on-close job, so nothing it
		// starts can escape the job (and outlive a revoked session).
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED,
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
	job, jerr := jobAndResume(cmd.Process.Pid)
	if jerr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, jerr
	}
	var jobOnce sync.Once
	closeJob := func() {
		jobOnce.Do(func() {
			if jerr == nil {
				windows.CloseHandle(job) // kills the whole tree
			}
		})
	}
	go func() {
		_, _ = io.Copy(stdin, ch)
		stdin.Close()
	}()
	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		closeJob()
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
			defer closeJob()
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
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED,
		envBlock(env), dirp, &si.StartupInfo, &pi)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("starting shell: %w", err)
	}
	// Put the shell in a kill-on-close job before it runs, so nothing it
	// starts outlives the session.
	job, jerr := newKillJob()
	if jerr == nil {
		if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
			windows.CloseHandle(job)
			jerr = err
		}
	}
	if jerr != nil { // fail closed: never run a shell that could outlive the session
		_ = windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("starting shell: %w", jerr)
	}
	_, _ = windows.ResumeThread(pi.Thread)
	windows.CloseHandle(pi.Thread)
	var jobOnce sync.Once
	closeJob := func() {
		jobOnce.Do(func() {
			if jerr == nil {
				windows.CloseHandle(job)
			}
		})
	}
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
			closeJob()
			return int(code)
		},
		hangup: func() {
			_ = windows.TerminateProcess(pi.Process, 1)
			closeJob()
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

// newKillJob returns a Job object that kills every process in it when the
// handle is closed, so a session's whole process tree ends with the session.
func newKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

const processSuspendResume = 0x0800

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// jobAndResume puts a suspended process in a new kill-on-close job and then
// lets it run. It fails closed: a process that can't be put in the job is
// not resumed.
func jobAndResume(pid int) (windows.Handle, error) {
	job, err := newKillJob()
	if err != nil {
		return 0, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|processSuspendResume, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	if st, _, _ := ntResumeProcess.Call(uintptr(h)); st != 0 {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("resume process: NTSTATUS %#x", st)
	}
	return job, nil
}
