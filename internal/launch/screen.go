package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Screen-sharing protocols TailConnect can open through a tunnel.
const (
	ScreenRustDesk = "rustdesk" // RustDesk with "direct IP access" (TCP 21118)
	ScreenRDP      = "rdp"      // Windows / Linux Remote Desktop (TCP 3389)
	ScreenVNC      = "vnc"      // macOS Screen Sharing and other VNC servers (TCP 5900)
)

// ScreenViewer reports whether this computer can open kind. If it can't,
// hint says what to install.
func ScreenViewer(kind string) (ok bool, hint string) {
	if v := screenViewer(kind, 0); v != nil {
		return true, ""
	}
	switch kind {
	case ScreenRustDesk:
		return false, "install RustDesk (rustdesk.com) on this computer"
	case ScreenRDP:
		switch runtime.GOOS {
		case "darwin":
			return false, "install Microsoft's free Windows App from the Mac App Store"
		case "linux":
			return false, "install Remmina with its RDP plugin (e.g. sudo apt install remmina remmina-plugin-rdp)"
		}
	case ScreenVNC:
		switch runtime.GOOS {
		case "windows":
			return false, "Windows has no VNC viewer built in: install RustDesk on both computers, or a VNC viewer such as TigerVNC"
		case "linux":
			return false, "install Remmina with its VNC plugin (e.g. sudo apt install remmina remmina-plugin-vnc)"
		}
	}
	return false, "no viewer for " + kind + " on this computer"
}

// OpenScreen opens a viewer for kind connected to 127.0.0.1:port (the
// local end of a TailConnect tunnel).
func OpenScreen(kind string, port int) error {
	argv := screenViewer(kind, port)
	if argv == nil {
		_, hint := ScreenViewer(kind)
		return fmt.Errorf("can't open %s: %s", kind, hint)
	}
	return start(argv[0], argv[1:]...)
}

// screenViewer returns the command that opens kind at 127.0.0.1:port, or
// nil when no viewer is installed. Programs come from PATH (never the
// current directory) or fixed install locations.
func screenViewer(kind string, port int) []string {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	switch kind {
	case ScreenRustDesk:
		// An IP:port target makes RustDesk connect directly over TCP (no
		// RustDesk servers); a running RustDesk takes over the request.
		if bin := rustDeskBin(); bin != nil {
			return append(bin, "--connect", addr)
		}
	case ScreenRDP:
		switch runtime.GOOS {
		case "windows":
			return []string{"mstsc", "/v:" + addr}
		case "darwin":
			if appInstalled("Windows App") || appInstalled("Microsoft Remote Desktop") {
				return []string{"open", "rdp://full%20address=s:" + addr}
			}
		default:
			if p, err := LookPath("remmina"); err == nil {
				return []string{p, "-c", "rdp://" + addr}
			}
			for _, n := range []string{"xfreerdp3", "xfreerdp", "wlfreerdp"} {
				if p, err := LookPath(n); err == nil {
					return []string{p, "/v:" + addr, "/dynamic-resolution"}
				}
			}
			if p, err := LookPath("gnome-connections"); err == nil {
				return []string{p, "rdp://" + addr}
			}
		}
	case ScreenVNC:
		hostPort := "127.0.0.1::" + strconv.Itoa(port) // VNC convention: "::" = port, ":" = display
		switch runtime.GOOS {
		case "darwin":
			return []string{"open", "vnc://" + addr} // Screen Sharing is built in
		case "windows":
			for _, rel := range []string{`RealVNC\VNC Viewer\vncviewer.exe`, `TigerVNC\vncviewer.exe`, `uvnc bvba\UltraVNC\vncviewer.exe`} {
				if p := programFile(rel); p != "" {
					return []string{p, hostPort}
				}
			}
			if p := programFile(`TightVNC\tvnviewer.exe`); p != "" {
				return []string{p, "-host=127.0.0.1", "-port=" + strconv.Itoa(port)}
			}
			if p, err := exec.LookPath("vncviewer.exe"); err == nil {
				return []string{p, hostPort}
			}
		default:
			if p, err := LookPath("remmina"); err == nil {
				return []string{p, "-c", "vnc://" + addr}
			}
			if p, err := LookPath("vncviewer"); err == nil {
				return []string{p, hostPort}
			}
			if p, err := LookPath("gnome-connections"); err == nil {
				return []string{p, "vnc://" + addr}
			}
		}
	}
	return nil
}

// OpenPrivacyPane opens macOS's Privacy & Security settings at Screen
// Recording ("screen") or Accessibility ("accessibility").
func OpenPrivacyPane(pane string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	anchor := map[string]string{"screen": "Privacy_ScreenCapture", "accessibility": "Privacy_Accessibility"}[pane]
	if anchor == "" {
		return fmt.Errorf("unknown settings pane %q", pane)
	}
	return start("open", "x-apple.systempreferences:com.apple.preference.security?"+anchor)
}

// OpenApp opens a macOS app by name (no-op elsewhere).
func OpenApp(name string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return start("open", "-a", name)
}

// OpenRustDesk connects the RustDesk viewer here to 127.0.0.1:port (a
// one-shot tunnel to another computer's RustDesk), signing in with password
// if known. RustDesk is started first: a "--connect" request is then handed
// to the running app and its process exits at once, so the password is on
// a command line only for an instant (a cold start would keep it there for
// as long as RustDesk runs).
func OpenRustDesk(port int, password string) error {
	bin := rustDeskBin()
	if bin == nil {
		_, hint := ScreenViewer(ScreenRustDesk)
		return fmt.Errorf("can't open RustDesk: %s", hint)
	}
	if !rustDeskRunning() {
		if runtime.GOOS == "darwin" {
			_ = start("open", "-a", "RustDesk")
		} else {
			_ = start(bin[0], bin[1:]...)
		}
		for i := 0; i < 30 && !rustDeskRunning(); i++ {
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(2 * time.Second) // let it finish starting before handing over
	}
	argv := append(slices.Clone(bin), "--connect", "127.0.0.1:"+strconv.Itoa(port))
	if password != "" {
		argv = append(argv, "--password", password)
	}
	return start(argv[0], argv[1:]...)
}

// RustDeskRunning reports whether the RustDesk app runs for this user.
func RustDeskRunning() bool { return rustDeskRunning() }

// StartRustDesk starts RustDesk in the background (hidden on macOS) if it
// isn't running. On a Mac without RustDesk's optional system service, it
// accepts connections only while its app runs.
func StartRustDesk() error {
	if rustDeskRunning() {
		return nil
	}
	bin := rustDeskBin()
	if bin == nil {
		return errors.New("RustDesk isn't installed")
	}
	if runtime.GOOS == "darwin" {
		// bin is …/RustDesk.app/Contents/MacOS/RustDesk.
		return start("open", "-g", "-j", filepath.Dir(filepath.Dir(filepath.Dir(bin[0]))))
	}
	return start(bin[0], bin[1:]...)
}

// rustDeskRunning reports whether the RustDesk app (not only its service)
// is running for this user.
func rustDeskRunning() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("pgrep", "-x", "-U", strconv.Itoa(os.Getuid()), "RustDesk").Run() == nil
	case "windows":
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq rustdesk.exe", "/FI", "USERNAME eq "+os.Getenv("USERNAME"), "/NH").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), "rustdesk.exe")
	default:
		// The UI runs without --service/--server arguments.
		out, err := exec.Command("pgrep", "-a", "-u", strconv.Itoa(os.Getuid()), "-f", "rustdesk").Output()
		if err != nil {
			return false
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l != "" && !strings.Contains(l, "--service") && !strings.Contains(l, "--server") && !strings.Contains(l, "--tray") {
				return true
			}
		}
		return false
	}
}

func rustDeskBin() []string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			p := filepath.Join(dir, "RustDesk.app", "Contents", "MacOS", "RustDesk")
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && trustedFile(p) {
				return []string{p}
			}
		}
	case "windows":
		if p := programFile(`RustDesk\rustdesk.exe`); p != "" {
			return []string{p}
		}
		if p, err := exec.LookPath("rustdesk.exe"); err == nil {
			return []string{p}
		}
	default:
		if p, err := LookPath("rustdesk"); err == nil {
			return []string{p}
		}
		if fp, err := LookPath("flatpak"); err == nil && exec.Command(fp, "info", "com.rustdesk.RustDesk").Run() == nil {
			return []string{fp, "run", "com.rustdesk.RustDesk"}
		}
	}
	return nil
}

// appInstalled reports whether a macOS app bundle is installed.
func appInstalled(name string) bool {
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
		if fi, err := os.Stat(filepath.Join(dir, name+".app")); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// programFile finds rel under the Windows Program Files folders.
func programFile(rel string) string {
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramW6432")} {
		if root == "" {
			continue
		}
		p := filepath.Join(root, rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
