// Package hostops performs local operations other devices can request
// through the agent API: listing listening services, processes and Docker
// containers, killing processes, Wake-on-LAN, clipboard and file receipt.
// It also parses the equivalent output from SSH-only hosts.
package hostops

import (
	"bufio"
	"context"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/imbikramsaha/tailconnect/internal/agent"
)

// known maps well-known ports to a label and whether it is a web UI.
var known = map[int]struct {
	label string
	web   bool
}{
	22: {"SSH", false}, 80: {"Web server", true}, 443: {"Web server (HTTPS)", true},
	1234: {"LM Studio", true}, 3000: {"Web app", true}, 3001: {"Web app", true},
	3306: {"MySQL", false}, 3389: {"Remote Desktop", false}, 4200: {"Angular dev server", true},
	5000: {"Web app", true}, 5173: {"Vite dev server", true}, 5432: {"PostgreSQL", false},
	5900: {"Screen Sharing (VNC)", false}, 6006: {"TensorBoard", true}, 6379: {"Redis", false},
	7860: {"Gradio / Stable Diffusion", true}, 8000: {"Web app / vLLM", true}, 8080: {"Web app", true},
	8188: {"ComfyUI", true}, 8265: {"Ray dashboard", true}, 8501: {"Streamlit", true},
	8888: {"Jupyter", true}, 8889: {"Jupyter", true}, 9000: {"Web app", true}, 9090: {"Prometheus", true},
	11434: {"Ollama API", true}, 27017: {"MongoDB", false},
}

// byProcess refines labels from the process name.
var byProcess = []struct{ match, label string }{
	{"jupyter", "Jupyter"}, {"ollama", "Ollama API"}, {"code-server", "code-server"},
	{"tensorboard", "TensorBoard"}, {"streamlit", "Streamlit"}, {"comfy", "ComfyUI"},
	{"vllm", "vLLM"}, {"lm studio", "LM Studio"}, {"grafana", "Grafana"}, {"postgres", "PostgreSQL"},
	{"redis", "Redis"}, {"mysqld", "MySQL"}, {"mongod", "MongoDB"}, {"sshd", "SSH"},
	{"docker", "Docker container"}, {"com.docker", "Docker container"}, {"node", "Node.js app"},
}

// noise lists OS daemons that listen but are not useful to forward.
var noise = []string{
	"rapportd", "controlce", "sharingd", "identityservicesd", "mdnsresponder", "remoted",
	"cupsd", "launchd", "airplayxpchelper", "systemd-resolve", "avahi", "rpcbind", "chronyd",
	"tailconnect", "tailscaled", "spoolsv", "lsass", "svchost", "wininit", "services", "system",
	// desktop apps' internal ports
	"code helper", "cursor helper", "google chrome helper", "microsoft edge helper", "slack", "spotify",
	"dropbox", "figma", "zoom", "onedrive", "steam", "raycast", "1password", "adobe", "discord", "teams",
}

// Label guesses what a service is.
func Label(port int, proc string) (string, bool) {
	lp := strings.ToLower(proc)
	k, ok := known[port]
	for _, b := range byProcess {
		if strings.Contains(lp, b.match) {
			web := true
			if ok {
				web = k.web
			}
			switch b.label {
			case "PostgreSQL", "Redis", "MySQL", "MongoDB", "SSH":
				web = false
			}
			return b.label, web
		}
	}
	if ok {
		return k.label, k.web
	}
	return "", port >= 1024
}

func isNoise(proc string) bool {
	lp := strings.ToLower(proc)
	for _, n := range noise {
		if strings.HasPrefix(lp, n) {
			return true
		}
	}
	return false
}

// reachable reports whether a tunnel (which dials loopback) can reach a
// service bound to addr.
func reachable(addr string) bool {
	addr = strings.Trim(addr, "[]")
	if addr == "*" || addr == "" {
		return true
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	// Tunnels dial 127.0.0.1 / ::1, so other loopback addresses (e.g.
	// systemd-resolved's 127.0.0.53) are unreachable through them.
	return ip == netip.MustParseAddr("127.0.0.1") || ip == netip.IPv6Loopback() || ip.IsUnspecified() ||
		(ip.Is4In6() && ip.Unmap() == netip.MustParseAddr("127.0.0.1"))
}

// finish filters, labels, dedupes and sorts services.
func finish(in []agent.Service, selfPID int) []agent.Service {
	seen := map[int]bool{}
	var out []agent.Service
	for _, s := range in {
		if s.Port <= 0 || seen[s.Port] || s.Port >= 49152 || s.PID == selfPID ||
			isNoise(s.Process) || !reachable(s.Address) {
			continue
		}
		seen[s.Port] = true
		s.Label, s.Web = Label(s.Port, s.Process)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Web != out[j].Web {
			return out[i].Web
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// Services lists TCP ports listening on this machine.
func Services(ctx context.Context) ([]agent.Service, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var raw []agent.Service
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn").Output()
		if err != nil && len(out) == 0 {
			return nil, err
		}
		raw = ParseLsof(string(out))
	} else {
		conns, err := gnet.ConnectionsWithContext(ctx, "tcp")
		if err != nil {
			return nil, err
		}
		names := map[int32]string{}
		for _, c := range conns {
			if c.Status != "LISTEN" {
				continue
			}
			name, ok := names[c.Pid]
			if !ok && c.Pid > 0 {
				if p, err := process.NewProcessWithContext(ctx, c.Pid); err == nil {
					name, _ = p.NameWithContext(ctx)
				}
				names[c.Pid] = name
			}
			raw = append(raw, agent.Service{Port: int(c.Laddr.Port), Address: c.Laddr.IP, PID: int(c.Pid), Process: name})
		}
	}
	return finish(raw, os.Getpid()), nil
}

// ParseLsof parses `lsof -nP -iTCP -sTCP:LISTEN -F pcn` output.
func ParseLsof(out string) []agent.Service {
	var res []agent.Service
	pid, cmd := 0, ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			pid, _ = strconv.Atoi(l[1:])
		case 'c':
			cmd = l[1:]
		case 'n':
			name := l[1:]
			i := strings.LastIndexByte(name, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(name[i+1:])
			if err != nil {
				continue
			}
			res = append(res, agent.Service{Port: port, Address: name[:i], PID: pid, Process: cmd})
		}
	}
	return res
}

// RemoteServicesCmd lists listening ports on an SSH-only Linux/macOS host.
const RemoteServicesCmd = `if command -v ss >/dev/null 2>&1; then ss -Hltnp 2>/dev/null | sed 's/^/ss /'; ` +
	`elif [ -x /usr/sbin/lsof ]; then /usr/sbin/lsof -nP -iTCP -sTCP:LISTEN -F pcn 2>/dev/null | sed 's/^/lsof /'; fi; true`

// ParseRemoteServices parses RemoteServicesCmd output.
func ParseRemoteServices(out string) []agent.Service {
	var raw []agent.Service
	var lsof strings.Builder
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		kind, rest, _ := strings.Cut(sc.Text(), " ")
		switch kind {
		case "lsof":
			lsof.WriteString(rest + "\n")
		case "ss":
			// LISTEN 0 4096 127.0.0.1:8888 0.0.0.0:* users:(("jupyter-lab",pid=123,fd=5))
			f := strings.Fields(rest)
			if len(f) < 4 {
				continue
			}
			local := f[3]
			i := strings.LastIndexByte(local, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(local[i+1:])
			if err != nil {
				continue
			}
			addr := local[:i]
			if j := strings.IndexByte(addr, '%'); j >= 0 { // 127.0.0.53%lo
				addr = addr[:j]
			}
			s := agent.Service{Port: port, Address: addr}
			if j := strings.Index(rest, `(("`); j >= 0 {
				r := rest[j+3:]
				if k := strings.IndexByte(r, '"'); k >= 0 {
					s.Process = r[:k]
				}
				if k := strings.Index(r, "pid="); k >= 0 {
					s.PID, _ = strconv.Atoi(strings.TrimRight(strings.SplitN(r[k+4:], ",", 2)[0], ")"))
				}
			}
			raw = append(raw, s)
		}
	}
	raw = append(raw, ParseLsof(lsof.String())...)
	return finish(raw, -1)
}
