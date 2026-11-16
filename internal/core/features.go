package core

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/forward"
	"github.com/imbikramsaha/tailconnect/internal/hostops"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/logx"
	"github.com/imbikramsaha/tailconnect/internal/sshx"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// peerIP returns a saved peer's tailnet IP if it is online.
func (c *Core) peerIP(dev store.Device) (string, error) {
	p, ok := c.peer(dev.NodeID)
	if !ok || !p.Online || p.IP == "" {
		return "", fmt.Errorf("%s is offline", dev.Name)
	}
	return p.IP, nil
}

// ---- detected services & ad-hoc forwards ----

// Services lists services listening on a device.
func (c *Core) Services(ctx context.Context, id string) ([]agent.Service, error) {
	if id == "self" {
		return hostops.Services(ctx)
	}
	dev, ok := c.device(id)
	if !ok {
		return nil, errors.New("unknown device")
	}
	if dev.Kind == store.KindPeer {
		ip, err := c.peerIP(dev)
		if err != nil {
			return nil, err
		}
		return c.agentCli.Services(ctx, ip)
	}
	out, err := c.runRemote(ctx, dev, hostops.RemoteServicesCmd)
	if err != nil {
		return nil, err
	}
	return hostops.ParseRemoteServices(out), nil
}

// runRemote runs a command on an SSH-only host.
func (c *Core) runRemote(ctx context.Context, dev store.Device, cmd string) (string, error) {
	if err := c.ensureConnected(ctx, dev.ID); err != nil {
		return "", err
	}
	cl, err := c.sshClient(ctx, dev)
	if err != nil {
		return "", err
	}
	return sshx.Run(ctx, cl, cmd, nil)
}

// Forward exposes a device port on localhost without a saved app (used for
// detected services and remote desktop). It returns the running entry.
func (c *Core) Forward(ctx context.Context, deviceID string, port int, label string, open bool) (*running, error) {
	dev, ok := c.device(deviceID)
	if !ok {
		return nil, errors.New("unknown device")
	}
	if err := c.ensureConnected(ctx, deviceID); err != nil {
		return nil, err
	}
	cs := c.conn(deviceID)
	if cs == nil {
		return nil, errors.New("not connected")
	}
	key := "port-" + strconv.Itoa(port)
	if label == "" {
		label = "Port " + strconv.Itoa(port)
	}
	c.mu.Lock()
	if r := cs.apps[key]; r != nil {
		c.mu.Unlock()
		if open && !c.opts.Headless {
			_ = c.opts.OpenURL(r.URL)
		}
		return r, nil
	}
	r := &running{AppID: key, Name: label, RemotePort: port, Status: "starting", StartedAt: time.Now(), AdHoc: true}
	cs.apps[key] = r
	c.mu.Unlock()
	f, err := forward.Start(port, func(ctx context.Context) (net.Conn, error) {
		d, ok := c.device(deviceID)
		if !ok {
			return nil, errors.New("device removed")
		}
		return c.dialPort(ctx, d, port)
	}, c.logs.Printf)
	if err != nil {
		c.mu.Lock()
		delete(cs.apps, key)
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	r.fwd, r.LocalPort = f, f.Port
	r.URL = "http://localhost:" + strconv.Itoa(f.Port) + "/"
	r.Status = "running"
	c.mu.Unlock()
	c.logs.Session.Printf("forwarded %s on %q: remote :%d -> localhost:%d", label, dev.Name, port, f.Port)
	c.events.Notify()
	if open && !c.opts.Headless {
		_ = c.opts.OpenURL(r.URL)
	}
	return r, nil
}

// ---- processes & docker ----

// Processes lists a device's busiest processes.
func (c *Core) Processes(ctx context.Context, id string) ([]agent.Process, error) {
	if id == "self" {
		return hostops.Processes(ctx)
	}
	dev, ok := c.device(id)
	if !ok {
		return nil, errors.New("unknown device")
	}
	if dev.Kind == store.KindPeer {
		ip, err := c.peerIP(dev)
		if err != nil {
			return nil, err
		}
		return c.agentCli.Processes(ctx, ip)
	}
	out, err := c.runRemote(ctx, dev, hostops.RemoteProcsCmd)
	if err != nil {
		return nil, err
	}
	return hostops.ParseRemoteProcs(out), nil
}

// Kill stops a process on a device.
func (c *Core) Kill(ctx context.Context, id string, pid int) error {
	dev, ok := c.device(id)
	if !ok {
		return errors.New("unknown device")
	}
	if pid <= 1 {
		return errors.New("invalid process")
	}
	if dev.Kind == store.KindPeer {
		ip, err := c.peerIP(dev)
		if err != nil {
			return err
		}
		err = c.agentCli.Kill(ctx, ip, pid)
		if err == nil {
			c.logs.Session.Printf("stopped process %d on %q", pid, dev.Name)
		}
		return err
	}
	_, err := c.runRemote(ctx, dev, "kill "+strconv.Itoa(pid))
	return err
}

// Containers lists Docker containers on a device.
func (c *Core) Containers(ctx context.Context, id string) ([]agent.Container, error) {
	dev, ok := c.device(id)
	if !ok {
		return nil, errors.New("unknown device")
	}
	if dev.Kind == store.KindPeer {
		ip, err := c.peerIP(dev)
		if err != nil {
			return nil, err
		}
		return c.agentCli.Containers(ctx, ip)
	}
	out, err := c.runRemote(ctx, dev, "command -v docker >/dev/null 2>&1 || { echo 'Docker is not installed' >&2; exit 3; }; "+hostops.DockerPSCmd)
	if err != nil {
		return nil, err
	}
	return hostops.ParseContainers(out), nil
}

// ContainerAction starts/stops/restarts a container on a device.
func (c *Core) ContainerAction(ctx context.Context, id, container, action string) error {
	if err := hostops.ValidContainerAction(container, action); err != nil {
		return err
	}
	dev, ok := c.device(id)
	if !ok {
		return errors.New("unknown device")
	}
	var err error
	if dev.Kind == store.KindPeer {
		var ip string
		if ip, err = c.peerIP(dev); err == nil {
			err = c.agentCli.ContainerAction(ctx, ip, container, action)
		}
	} else {
		_, err = c.runRemote(ctx, dev, "docker "+action+" "+container)
	}
	if err == nil {
		c.logs.Session.Printf("docker %s %s on %q", action, container, dev.Name)
		c.events.Notify()
	}
	return err
}

// ---- Wake-on-LAN ----

// wakePlanLocked finds how to wake dev: from this machine (relay "") or via
// an online paired peer on the same LAN. c.mu must be held.
func (c *Core) wakePlanLocked(dev store.Device, selfLANs []string) (relayIP, broadcast string, ok bool) {
	if len(dev.MACs) == 0 || len(dev.LANs) == 0 {
		return "", "", false
	}
	for _, l := range dev.LANs {
		for _, m := range selfLANs {
			if l == m {
				return "", broadcastOf(l), true
			}
		}
	}
	for _, p := range c.peers {
		if !p.Online || p.Hello == nil || p.NodeID == dev.NodeID || store.AccessRank(p.Hello.Access) < 2 {
			continue
		}
		for _, l := range dev.LANs {
			for _, m := range p.Hello.LANs {
				if l == m {
					return p.IP, broadcastOf(l), true
				}
			}
		}
	}
	return "", "", false
}

func broadcastOf(cidr string) string {
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil || !pfx.Addr().Is4() {
		return ""
	}
	a := pfx.Masked().Addr().As4()
	bits := pfx.Bits()
	for i := 0; i < 4; i++ {
		for b := 0; b < 8; b++ {
			if i*8+b >= bits {
				a[i] |= 1 << (7 - b)
			}
		}
	}
	return netip.AddrFrom4(a).String()
}

// Wake sends Wake-on-LAN packets for an offline device, from this machine
// or through another device on its network. It returns who sent them.
func (c *Core) Wake(ctx context.Context, id string) (string, error) {
	dev, ok := c.device(id)
	if !ok {
		return "", errors.New("unknown device")
	}
	if len(dev.MACs) == 0 {
		return "", errors.New("TailConnect hasn't learned this device's network address yet; it's recorded the next time the device is online")
	}
	_, selfLANs := c.lanInfo()
	c.mu.Lock()
	relay, bcast, ok := c.wakePlanLocked(dev, selfLANs)
	c.mu.Unlock()
	if !ok {
		return "", errors.New("no device on " + dev.Name + "'s network is online to send the wake signal")
	}
	via := "this computer"
	var err error
	if relay == "" {
		err = hostops.Wake(dev.MACs, bcast)
	} else {
		err = c.agentCli.Wake(ctx, relay, agent.WakeRequest{MACs: dev.MACs, Broadcast: bcast})
		via = relay
		c.mu.Lock()
		for _, p := range c.peers {
			if p.IP == relay {
				via = displayName(*p)
			}
		}
		c.mu.Unlock()
	}
	if err != nil {
		return "", err
	}
	c.logs.Session.Printf("sent wake signal to %q via %s", dev.Name, via)
	go func() { // poll more often while it boots
		for i := 0; i < 24; i++ {
			time.Sleep(5 * time.Second)
			c.Kick()
		}
	}()
	return via, nil
}

// ---- remote desktop ----

// RemoteDesktop forwards the device's screen-sharing port and opens a client.
func (c *Core) RemoteDesktop(ctx context.Context, id string) (string, error) {
	dev, ok := c.device(id)
	if !ok {
		return "", errors.New("unknown device")
	}
	if c.opts.Headless {
		return "", errors.New("not available in headless mode")
	}
	if err := c.ensureConnected(ctx, id); err != nil {
		return "", err
	}
	// RustDesk first when both ends have it (it works between every pair of
	// systems, Windows Home included), then the system's own remote desktop.
	type cand struct {
		port        int
		kind, label string
	}
	cands := []cand{{21118, launch.ScreenRustDesk, "RustDesk"}, {3389, launch.ScreenRDP, "Remote Desktop"}, {5900, launch.ScreenVNC, "VNC"}}
	if dev.OS == "darwin" {
		cands = []cand{cands[0], {5900, launch.ScreenVNC, "Screen Sharing"}}
	}
	var missing []string
	for _, cd := range cands {
		if !c.remotePortOpen(ctx, dev, cd.port) {
			continue
		}
		if ok, hint := launch.ScreenViewer(cd.kind); !ok {
			missing = append(missing, cd.label+": "+hint)
			continue
		}
		r, err := c.Forward(ctx, id, cd.port, cd.label, false)
		if err != nil {
			return "", err
		}
		return cd.label, launch.OpenScreen(cd.kind, r.LocalPort)
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("%s is ready for screen sharing, but this computer needs a viewer. %s", dev.Name, strings.Join(missing, ". Or "))
	}
	rustdesk := "install RustDesk on both computers and turn on its Settings → Security → Enable direct IP access (works on every system, including Windows Home)"
	hint := "turn on Remote Desktop (GNOME: Settings → System → Remote Desktop), or " + rustdesk
	switch dev.OS {
	case "darwin":
		hint = "turn on System Settings → General → Sharing → Screen Sharing, or " + rustdesk
	case "windows":
		hint = "turn on Settings → System → Remote Desktop (Windows Pro/Enterprise), or " + rustdesk
	}
	return "", fmt.Errorf("screen sharing isn't enabled on %s: %s", dev.Name, hint)
}

// ---- diagnostics ----

// Diagnosis describes a device's connection quality.
type Diagnosis struct {
	Path      string    `json:"path"` // direct | relay | peer-relay | lan | internet
	Endpoint  string    `json:"endpoint,omitempty"`
	Relay     string    `json:"relay,omitempty"`
	LatencyMS []float64 `json:"latencyMS"`
	SpeedMbps float64   `json:"speedMbps,omitempty"`
	SpeedErr  string    `json:"speedErr,omitempty"`
	Advice    []string  `json:"advice"`
}

// Diagnose measures latency, path and throughput to a device.
func (c *Core) Diagnose(ctx context.Context, id string) (*Diagnosis, error) {
	dev, ok := c.device(id)
	if !ok {
		return nil, errors.New("unknown device")
	}
	d := &Diagnosis{LatencyMS: []float64{}, Advice: []string{}}
	tailnetIP := ""
	if dev.Kind == store.KindPeer {
		ip, err := c.peerIP(dev)
		if err != nil {
			return nil, err
		}
		tailnetIP = ip
	} else if dev.Via == store.ViaTailnet {
		tailnetIP = dev.Host
		if p := c.findPeerByHost(dev.Host); p != "" {
			tailnetIP = p
		}
	}
	if tailnetIP != "" {
		addr, err := netip.ParseAddr(tailnetIP)
		if err != nil {
			return nil, fmt.Errorf("bad tailnet address %q", tailnetIP)
		}
		for i := 0; i < 4; i++ {
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			r, err := c.node.Ping(pctx, addr)
			cancel()
			if err != nil || r.Err != "" {
				continue
			}
			if i == 0 {
				continue // first ping often pays for path setup
			}
			d.LatencyMS = append(d.LatencyMS, r.LatencySeconds*1000)
			switch {
			case r.Endpoint != "":
				d.Path, d.Endpoint = "direct", r.Endpoint
			case r.PeerRelay != "":
				d.Path, d.Relay = "peer-relay", r.PeerRelay
			case r.DERPRegionCode != "":
				d.Path, d.Relay = "relay", r.DERPRegionCode
			}
		}
		if len(d.LatencyMS) == 0 {
			d.Advice = append(d.Advice, "The device didn't answer Tailscale pings. It may be asleep or its network may block Tailscale.")
		}
	} else {
		d.Path = "internet"
		for i := 0; i < 3; i++ {
			start := time.Now()
			conn, err := c.DialDevice(ctx, id, dev.Port)
			if err != nil {
				return nil, err
			}
			d.LatencyMS = append(d.LatencyMS, float64(time.Since(start).Microseconds())/1000)
			conn.Close()
		}
	}
	if d.Path == "direct" {
		if ap, err := netip.ParseAddrPort(d.Endpoint); err == nil && (ap.Addr().IsPrivate() || ap.Addr().IsLoopback() || ap.Addr().IsLinkLocalUnicast()) {
			d.Path = "lan"
		}
	}
	// Throughput (peers only: the agent serves test data).
	if dev.Kind == store.KindPeer {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		bps, err := c.agentCli.Speed(sctx, tailnetIP, 16<<20)
		cancel()
		if err != nil {
			d.SpeedErr = err.Error()
		} else {
			d.SpeedMbps = bps * 8 / 1e6
		}
	}
	switch d.Path {
	case "relay":
		d.Advice = append(d.Advice, "Traffic is relayed through Tailscale's "+strings.ToUpper(d.Relay)+" server because the two networks couldn't connect directly. This is usually a strict firewall or NAT (common on office, hotel and mobile networks). Allowing outgoing UDP, or enabling UPnP/NAT-PMP on your router, usually gives a direct connection.")
	case "peer-relay":
		d.Advice = append(d.Advice, "Traffic goes through a peer relay in your tailnet — faster than Tailscale's public relays, but a direct connection wasn't possible.")
	case "lan":
		d.Advice = append(d.Advice, "Both devices are on the same local network — the fastest possible path.")
	case "direct":
		d.Advice = append(d.Advice, "Devices are connected directly, peer to peer, over the internet.")
	}
	if avg := avg(d.LatencyMS); avg > 150 {
		d.Advice = append(d.Advice, fmt.Sprintf("Latency is high (%.0f ms); terminals may feel sluggish. Physical distance and relaying both add delay.", avg))
	}
	if d.SpeedMbps > 0 && d.SpeedMbps < 20 {
		d.Advice = append(d.Advice, "Throughput is low; large transfers will be slow. Check the slower network's upload speed.")
	}
	return d, nil
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func (c *Core) findPeerByHost(host string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.peers {
		if host == p.IP || host == p.HostName || host == p.DNSName || host == shortName(p.DNSName) {
			return p.IP
		}
	}
	return ""
}

// ---- files & clipboard ----

// SendFile sends a file to a device's Downloads/TailConnect folder.
func (c *Core) SendFile(ctx context.Context, id, name string, size int64, r io.Reader) (string, error) {
	dev, ok := c.device(id)
	if !ok {
		return "", errors.New("unknown device")
	}
	name, err := hostops.SafeName(name)
	if err != nil {
		return "", err
	}
	if dev.Kind == store.KindPeer {
		ip, err := c.peerIP(dev)
		if err != nil {
			return "", err
		}
		if err := c.verifyPeer(ctx, dev, ip); err != nil {
			return "", err
		}
		res, err := c.agentCli.SendFile(ctx, ip, name, size, r)
		if err != nil {
			return "", err
		}
		c.logs.Session.Printf("sent %s (%d bytes) to %q", name, res.Size, dev.Name)
		return res.Path, nil
	}
	// SSH hosts: SFTP into ~/Downloads/TailConnect.
	if err := c.ensureConnected(ctx, id); err != nil {
		return "", err
	}
	cl, err := c.sshClient(ctx, dev)
	if err != nil {
		return "", err
	}
	sc, err := sftp.NewClient(cl)
	if err != nil {
		return "", fmt.Errorf("SFTP isn't available on %s: %w", dev.Name, err)
	}
	defer sc.Close()
	home := dev.Home
	if home == "" {
		if home, err = sc.Getwd(); err != nil {
			return "", err
		}
	}
	dir := path.Join(home, "Downloads", "TailConnect")
	if err := sc.MkdirAll(dir); err != nil {
		return "", err
	}
	dst := path.Join(dir, name)
	ext := path.Ext(name)
	for i := 1; ; i++ {
		if _, err := sc.Lstat(dst); err != nil {
			break
		}
		dst = path.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
	}
	f, err := sc.Create(dst)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	c.logs.Session.Printf("sent %s (%d bytes) to %q", name, n, dev.Name)
	return dst, nil
}

// PushClipboard sends this computer's clipboard to a device.
func (c *Core) PushClipboard(ctx context.Context, id string) error {
	dev, ip, err := c.clipPeer(id)
	if err != nil {
		return err
	}
	text, err := hostops.GetClipboard(ctx)
	if err != nil {
		return err
	}
	if err := c.agentCli.SetClipboard(ctx, ip, text); err != nil {
		return err
	}
	c.logs.Session.Printf("sent clipboard to %q", dev.Name)
	return nil
}

// PullClipboard copies a device's clipboard to this computer.
func (c *Core) PullClipboard(ctx context.Context, id string) (int, error) {
	dev, ip, err := c.clipPeer(id)
	if err != nil {
		return 0, err
	}
	text, err := c.agentCli.GetClipboard(ctx, ip)
	if err != nil {
		return 0, err
	}
	if err := hostops.SetClipboard(ctx, text); err != nil {
		return 0, err
	}
	c.logs.Session.Printf("copied clipboard from %q", dev.Name)
	return len(text), nil
}

func (c *Core) clipPeer(id string) (store.Device, string, error) {
	dev, ok := c.device(id)
	if !ok {
		return dev, "", errors.New("unknown device")
	}
	if dev.Kind != store.KindPeer {
		return dev, "", errors.New("clipboard sharing needs TailConnect on both devices")
	}
	ip, err := c.peerIP(dev)
	if err == nil {
		err = c.verifyPeer(context.Background(), dev, ip)
	}
	return dev, ip, err
}

// ---- activity & problem reports ----

// Activity returns the most recent session-log lines (newest first).
func (c *Core) Activity(n int) []string {
	if c.logs == nil || c.logs.Dir == "" {
		return []string{}
	}
	b, err := os.ReadFile(filepath.Join(c.logs.Dir, "session.log"))
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			out = append(out, strings.Replace(l, "session: ", "", 1))
		}
	}
	return out
}

// ReportBundle zips logs and a state summary for a bug report and returns
// its path (in Downloads). Nothing is uploaded.
func (c *Core) ReportBundle(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "TailConnect-report-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // logs: owner only
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	if c.logs != nil && c.logs.Dir != "" {
		entries, _ := os.ReadDir(c.logs.Dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			src, err := os.Open(filepath.Join(c.logs.Dir, e.Name()))
			if err != nil {
				continue
			}
			w, err := zw.Create("logs/" + e.Name())
			if err == nil {
				// Redact again: older lines may predate redaction at write time.
				b, _ := io.ReadAll(io.LimitReader(src, 20<<20))
				_, _ = io.WriteString(w, logx.Redact(string(b)))
			}
			src.Close()
		}
	}
	v := c.Snapshot(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "TailConnect %s on %s/%s (%s)\n", v.Version, runtime.GOOS, runtime.GOARCH, v.Self.Name)
	fmt.Fprintf(&b, "Tailscale: %s, tailnet %q, ip %s\n", v.Tailnet.State, v.Tailnet.Tailnet, v.Tailnet.IPv4)
	fmt.Fprintf(&b, "Built-in SSH: %v (supported %v); system SSH: %v on %d\n\nDevices:\n",
		v.Self.BuiltinSSH, v.Self.BuiltinSSHSupported, v.Self.SSH, v.Self.SSHPort)
	for _, d := range v.Devices {
		fmt.Fprintf(&b, "- %s [%s/%s] online=%v state=%s link=%q access=%s theirAccess=%s ssh=%v builtin=%v err=%q\n",
			d.Name, d.Section, d.Kind, d.Online, d.State, d.Link, d.Access, d.TheirAccess, d.SSH, d.BuiltinSSH, d.Error)
	}
	if w, err := zw.Create("summary.txt"); err == nil {
		_, _ = io.WriteString(w, b.String())
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	c.logs.Printf("wrote problem report %s", p)
	if !c.opts.Headless {
		_ = launch.Reveal(p)
	}
	return p, nil
}
