// Package launch opens terminals, editors and browsers on the local machine,
// pointed at a device's SSH alias.
package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Builtin is a built-in launcher.
type Builtin struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is "terminal", "vscode" (VS Code-family --remote), "zed" or "ui".
	Kind string   `json:"kind"`
	bins []string // PATH names
	mac  []string // absolute macOS paths
	win  []string // Windows paths relative to %LOCALAPPDATA% / %ProgramFiles%
}

// Builtins lists every built-in app TailConnect can launch.
var Builtins = []Builtin{
	{ID: "terminal", Name: "Terminal", Kind: "terminal"},
	{ID: "vscode", Name: "VS Code", Kind: "vscode", bins: []string{"code"},
		mac: []string{"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"},
		win: []string{`Programs\Microsoft VS Code\bin\code.cmd`, `Microsoft VS Code\bin\code.cmd`}},
	{ID: "vscode-insiders", Name: "VS Code Insiders", Kind: "vscode", bins: []string{"code-insiders"},
		mac: []string{"/Applications/Visual Studio Code - Insiders.app/Contents/Resources/app/bin/code-insiders",
			"/Applications/Visual Studio Code - Insiders.app/Contents/Resources/app/bin/code"},
		win: []string{`Programs\Microsoft VS Code Insiders\bin\code-insiders.cmd`}},
	{ID: "cursor", Name: "Cursor", Kind: "vscode", bins: []string{"cursor"},
		mac: []string{"/Applications/Cursor.app/Contents/Resources/app/bin/cursor"},
		win: []string{`Programs\cursor\resources\app\bin\cursor.cmd`}},
	{ID: "windsurf", Name: "Windsurf", Kind: "vscode", bins: []string{"windsurf"},
		mac: []string{"/Applications/Windsurf.app/Contents/Resources/app/bin/windsurf"},
		win: []string{`Programs\Windsurf\bin\windsurf.cmd`}},
	{ID: "zed", Name: "Zed", Kind: "zed", bins: []string{"zed", "zeditor", "zedit"},
		mac: []string{"/Applications/Zed.app/Contents/MacOS/cli"},
		win: []string{`Programs\Zed\bin\zed.exe`}},
}

// FindBuiltin returns the builtin with id.
func FindBuiltin(id string) (Builtin, bool) {
	for _, b := range Builtins {
		if b.ID == id {
			return b, true
		}
	}
	return Builtin{}, false
}

// extraDirs are searched in addition to PATH (apps started at login often
// get a minimal PATH).
func extraDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "linux":
		return []string{"/snap/bin", "/usr/local/bin", "/usr/bin", filepath.Join(home, ".local/bin"),
			"/var/lib/flatpak/exports/bin", filepath.Join(home, ".local/share/flatpak/exports/bin")}
	case "darwin":
		return []string{"/usr/local/bin", "/opt/homebrew/bin", filepath.Join(home, ".local/bin")}
	}
	return nil
}

// LookPath finds an executable in PATH or common install dirs.
func LookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range extraDirs() {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func (b Builtin) find() (string, error) {
	for _, n := range b.bins {
		if p, err := LookPath(n); err == nil {
			return p, nil
		}
	}
	switch runtime.GOOS {
	case "darwin":
		for _, p := range b.mac {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	case "windows":
		for _, root := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles")} {
			if root == "" {
				continue
			}
			for _, rel := range b.win {
				p := filepath.Join(root, rel)
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%s is not installed (or its command-line launcher is not on PATH)", b.Name)
}

// Available reports whether a builtin can be launched on this machine.
func (b Builtin) Available() bool {
	if b.Kind == "terminal" || b.Kind == "ui" {
		return true
	}
	_, err := b.find()
	return err == nil
}

// Target is where an app should connect.
type Target struct {
	Alias string // ssh alias from the managed config
	Home  string // remote home dir ("" if unknown)
}

// remotePath converts a remote home dir into a vscode-remote URI path.
func remotePath(home string) string {
	if home == "" {
		return ""
	}
	if len(home) >= 2 && home[1] == ':' { // Windows host: C:\Users\x -> /C:/Users/x
		return "/" + strings.ReplaceAll(home, `\`, "/")
	}
	return home
}

// App launches builtin id against t. terminal is the preferred terminal.
func App(id string, t Target, terminal string) error {
	b, ok := FindBuiltin(id)
	if !ok {
		return fmt.Errorf("unknown app %q", id)
	}
	switch b.Kind {
	case "terminal":
		return Terminal(terminal, []string{"ssh", t.Alias})
	case "vscode":
		bin, err := b.find()
		if err != nil {
			return err
		}
		args := []string{"--new-window"}
		if p := remotePath(t.Home); p != "" {
			args = append(args, "--folder-uri", "vscode-remote://ssh-remote+"+t.Alias+p)
		} else {
			args = append(args, "--remote", "ssh-remote+"+t.Alias)
		}
		return start(bin, args...)
	case "zed":
		bin, err := b.find()
		if err != nil {
			return err
		}
		return start(bin, "ssh://"+t.Alias+remotePath(t.Home))
	}
	return errors.New("cannot launch " + b.Name)
}

// SSHCommand builds the argv for an interactive ssh session to alias that
// optionally runs remoteCmd (with a TTY).
func SSHCommand(alias, remoteCmd string) []string {
	if remoteCmd == "" {
		return []string{"ssh", alias}
	}
	return []string{"ssh", "-t", alias, remoteCmd}
}

// shellJoin quotes argv for a POSIX shell.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
			q[i] = a
			continue
		}
		q[i] = "'" + strings.ReplaceAll(a, "'", `'"'"'`) + "'"
	}
	return strings.Join(q, " ")
}

// OpenURL opens url in the default browser.
func OpenURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return start("open", url)
	case "windows":
		return start("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return start("xdg-open", url)
	}
}

// OpenPath opens a folder in the system file manager.
func OpenPath(p string) error {
	switch runtime.GOOS {
	case "darwin":
		return start("open", p)
	case "windows":
		return start("explorer", p)
	default:
		return start("xdg-open", p)
	}
}

// AppWindow opens url as a standalone app window using a Chromium-based
// browser (Chrome, Edge, Brave, Chromium) when available, else the default
// browser. profileDir isolates the window's browser profile.
func AppWindow(url, profileDir string) error {
	for _, b := range chromiumCandidates() {
		p := b
		if !filepath.IsAbs(p) {
			lp, err := LookPath(p)
			if err != nil {
				continue
			}
			p = lp
		} else if _, err := os.Stat(p); err != nil {
			continue
		}
		args := []string{"--app=" + url, "--user-data-dir=" + profileDir, "--no-first-run",
			"--no-default-browser-check", "--window-size=1180,800", "--class=TailConnect"}
		if err := start(p, args...); err == nil {
			return nil
		}
	}
	return OpenURL(url)
}

func chromiumCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		var out []string
		for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`))
		}
		return out
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
			"microsoft-edge", "microsoft-edge-stable", "brave-browser"}
	}
}

// start runs a detached process.
func start(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap
	return nil
}

// Reveal shows a file in the system file manager.
func Reveal(p string) error {
	switch runtime.GOOS {
	case "darwin":
		return start("open", "-R", p)
	case "windows":
		return start("explorer", "/select,", p)
	default:
		return start("xdg-open", filepath.Dir(p))
	}
}

// OpenRDP opens a Remote Desktop client for host:port.
func OpenRDP(host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	switch runtime.GOOS {
	case "windows":
		return start("mstsc", "/v:"+addr)
	case "darwin":
		// Handled by Microsoft's "Windows App" / Remote Desktop.
		return start("open", "rdp://full%20address=s:"+addr)
	default:
		if p, err := LookPath("remmina"); err == nil {
			return start(p, "-c", "rdp://"+addr)
		}
		return start("xdg-open", "rdp://"+addr)
	}
}

// OpenVNC opens a VNC client (Screen Sharing on macOS) for host:port.
func OpenVNC(host string, port int) error {
	u := fmt.Sprintf("vnc://%s:%d", host, port)
	if runtime.GOOS == "linux" {
		if p, err := LookPath("remmina"); err == nil {
			return start(p, "-c", u)
		}
	}
	return OpenURL(u)
}
