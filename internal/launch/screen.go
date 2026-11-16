package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
