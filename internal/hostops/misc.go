package hostops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ---- Wake-on-LAN ----

// LANInfo returns MAC addresses and private IPv4 networks of physical
// interfaces, so paired devices can wake this one later.
func LANInfo() (macs, lans []string) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	skip := []string{"utun", "tailscale", "docker", "br-", "veth", "vmnet", "vboxnet", "bridge", "awdl", "llw", "ap", "anpi", "gif", "stf", "virbr", "zt", "wg"}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		name := strings.ToLower(ifc.Name)
		bad := false
		for _, s := range skip {
			if strings.HasPrefix(name, s) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		addrs, _ := ifc.Addrs()
		var nets []string
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !ipn.IP.IsPrivate() {
				continue
			}
			nw := &net.IPNet{IP: ipn.IP.Mask(ipn.Mask), Mask: ipn.Mask}
			nets = append(nets, ipn.IP.String()+"|"+nw.String())
		}
		if len(nets) == 0 {
			continue
		}
		for _, m := range interfaceMACs(ifc) {
			if !containsStr(macs, m) {
				macs = append(macs, m)
			}
		}
		for _, n := range nets {
			_, cidr, _ := strings.Cut(n, "|")
			lans = append(lans, cidr)
		}
	}
	return macs, lans
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// interfaceMACs returns usable MACs for an interface. macOS hides MACs from
// unprivileged APIs (returns 02:00:00:00:00:00), so ask ifconfig (active,
// possibly private address) and networksetup (hardware address) there.
func interfaceMACs(ifc net.Interface) []string {
	hw := ifc.HardwareAddr.String()
	placeholder := hw == "02:00:00:00:00:00" || hw == "00:00:00:00:00:00"
	if !placeholder && runtime.GOOS != "darwin" {
		return []string{hw}
	}
	var out []string
	if !placeholder {
		out = append(out, hw)
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if b, err := exec.CommandContext(ctx, "/sbin/ifconfig", ifc.Name).Output(); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Fields(l); len(f) >= 2 && f[0] == "ether" {
					out = appendMAC(out, f[1])
				}
			}
		}
		if b, err := exec.CommandContext(ctx, "/usr/sbin/networksetup", "-getmacaddress", ifc.Name).Output(); err == nil {
			if f := strings.Fields(string(b)); len(f) >= 3 {
				out = appendMAC(out, f[2])
			}
		}
	}
	return out
}

func appendMAC(list []string, m string) []string {
	hw, err := net.ParseMAC(m)
	if err != nil || len(hw) != 6 || hw.String() == "02:00:00:00:00:00" || containsStr(list, hw.String()) {
		return list
	}
	return append(list, hw.String())
}

// OnLAN reports whether this machine has an address in any of cidrs and
// returns the matching directed-broadcast address.
func OnLAN(cidrs []string) (string, bool) {
	_, mine := LANInfo()
	for _, c := range cidrs {
		for _, m := range mine {
			if c == m {
				_, n, err := net.ParseCIDR(c)
				if err != nil {
					continue
				}
				b := make(net.IP, 4)
				ip4 := n.IP.To4()
				for i := range b {
					b[i] = ip4[i] | ^n.Mask[i]
				}
				return b.String(), true
			}
		}
	}
	return "", false
}

// MagicPacket builds a Wake-on-LAN packet for mac.
func MagicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("invalid MAC address %q", mac)
	}
	p := bytes.Repeat([]byte{0xff}, 6)
	for i := 0; i < 16; i++ {
		p = append(p, hw...)
	}
	return p, nil
}

// Wake broadcasts magic packets for macs on the given broadcast address
// (and the limited broadcast address) to UDP ports 9 and 7.
func Wake(macs []string, broadcast string) error {
	var sent int
	var lastErr error
	targets := []string{"255.255.255.255"}
	if broadcast != "" && broadcast != "255.255.255.255" {
		if net.ParseIP(broadcast).To4() == nil {
			return errors.New("invalid broadcast address")
		}
		targets = append([]string{broadcast}, targets...)
	}
	for _, m := range macs {
		pkt, err := MagicPacket(m)
		if err != nil {
			lastErr = err
			continue
		}
		for _, t := range targets {
			for _, port := range []string{"9", "7"} {
				c, err := net.Dial("udp4", net.JoinHostPort(t, port))
				if err != nil {
					lastErr = err
					continue
				}
				if _, err := c.Write(pkt); err != nil {
					lastErr = err
				} else {
					sent++
				}
				c.Close()
			}
		}
	}
	if sent == 0 {
		if lastErr == nil {
			lastErr = errors.New("no MAC addresses known")
		}
		return lastErr
	}
	return nil
}

// ---- Clipboard ----

func clipboardCmds() (get, set []string, err error) {
	switch runtime.GOOS {
	case "darwin":
		return []string{"pbpaste"}, []string{"pbcopy"}, nil
	case "windows":
		return []string{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard -Raw"},
			[]string{"powershell.exe", "-NoProfile", "-Command", "$input | Set-Clipboard"}, nil
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return []string{"wl-paste", "--no-newline"}, []string{"wl-copy"}, nil
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return []string{"xclip", "-selection", "clipboard", "-o"}, []string{"xclip", "-selection", "clipboard"}, nil
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		return []string{"xsel", "--clipboard", "--output"}, []string{"xsel", "--clipboard", "--input"}, nil
	}
	return nil, nil, errors.New("no clipboard tool found (install wl-clipboard or xclip)")
}

const maxClipboard = 1 << 20

// GetClipboard returns the clipboard text.
func GetClipboard(ctx context.Context) (string, error) {
	get, _, err := clipboardCmds()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, get[0], get[1:]...).Output()
	if err != nil {
		return "", fmt.Errorf("reading clipboard: %w", err)
	}
	if len(out) > maxClipboard {
		return "", errors.New("clipboard is larger than 1 MB")
	}
	return string(out), nil
}

// SetClipboard replaces the clipboard text.
func SetClipboard(ctx context.Context, text string) error {
	if len(text) > maxClipboard {
		return errors.New("clipboard text is larger than 1 MB")
	}
	_, set, err := clipboardCmds()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, set[0], set[1:]...)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("writing clipboard: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- Files ----

// InboxDir is where files sent from other devices are saved.
func InboxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads", "TailConnect"), nil
}

// SafeName reduces a client-supplied file name to a safe base name.
func SafeName(name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Map(func(r rune) rune {
		if (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			return -1 // bidi overrides disguise extensions ("txt.exe")
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	name = strings.TrimRight(name, ". ") // Windows strips these silently
	// filepath.Base returns the separator itself for "/" (and "\" on Windows).
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid file name")
	}
	// Windows reserved device names (CON, NUL, COM1…), with any extension.
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	name = strings.ToValidUTF8(name, "_")
	if r := []rune(name); len(r) > 200 {
		ext := []rune(filepath.Ext(name))
		if len(ext) > 16 {
			ext = nil // not a real extension
		}
		name = string(r[:200-len(ext)]) + string(ext)
	}
	return name, nil
}

// Receive saves r as name in dir without overwriting existing files.
func Receive(dir, name string, r io.Reader) (string, int64, error) {
	name, err := SafeName(name)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", 0, err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		dst := filepath.Join(dir, cand)
		// Defence in depth: the result must be a direct child of dir.
		if rel, err := filepath.Rel(dir, dst); err != nil || rel != filepath.Base(dst) || strings.HasPrefix(rel, "..") ||
			!strings.HasPrefix(filepath.Clean(dst), filepath.Clean(dir)+string(filepath.Separator)) {
			os.Remove(tmp.Name())
			return "", 0, errors.New("invalid file name")
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.Link(tmp.Name(), dst); err == nil {
			os.Remove(tmp.Name())
			quarantine(dst)
			return dst, n, nil
		} else if !os.IsExist(err) {
			// Filesystems without hard links: fall back to rename.
			if err := os.Rename(tmp.Name(), dst); err != nil {
				os.Remove(tmp.Name())
				return "", 0, err
			}
			quarantine(dst)
			return dst, n, nil
		}
	}
	os.Remove(tmp.Name())
	return "", 0, errors.New("too many files with that name")
}

// quarantine marks a received file as downloaded from elsewhere so macOS
// Gatekeeper checks it before it can run (like files from a browser).
func quarantine(path string) {
	if runtime.GOOS != "darwin" {
		return
	}
	val := fmt.Sprintf("0081;%08x;TailConnect;", time.Now().Unix())
	_ = exec.Command("/usr/bin/xattr", "-w", "com.apple.quarantine", val, path).Run()
}
