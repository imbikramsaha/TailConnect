package sysinfo

import (
	"bufio"
	"strconv"
	"strings"
	"time"
)

// RemoteScript gathers stats on a Linux host over SSH (for devices that do
// not run TailConnect). Output is parsed by ParseRemote.
const RemoteScript = `export LC_ALL=C
echo "host $(hostname)"
echo "os $(uname -s)"
echo "kernel $(uname -r)"
echo "arch $(uname -m)"
[ -r /etc/os-release ] && . /etc/os-release && echo "platform $PRETTY_NAME"
[ -r /proc/uptime ] && echo "uptime $(cut -d. -f1 /proc/uptime)"
[ -r /proc/loadavg ] && echo "load $(cut -d' ' -f1-3 /proc/loadavg)"
grep -m1 'model name' /proc/cpuinfo 2>/dev/null | sed 's/.*: /cpumodel /'
grep '^cpu' /proc/stat 2>/dev/null | sed 's/^/s1 /'
sleep 0.5
grep '^cpu' /proc/stat 2>/dev/null | sed 's/^/s2 /'
grep -E '^(MemTotal|MemAvailable|SwapTotal|SwapFree):' /proc/meminfo 2>/dev/null | sed 's/^/mem /'
df -P -B1 -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | tail -n +2 | sed 's/^/df /'
awk 'NR>2 && $1 !~ /lo:/ {gsub(":"," "); rx+=$2; tx+=$10} END {print "net", rx+0, tx+0}' /proc/net/dev 2>/dev/null
command -v nvidia-smi >/dev/null 2>&1 && ` + GPUQuery + ` 2>/dev/null | sed 's/^/gpu /'
true
`

// ParseRemote converts RemoteScript output into Stats.
func ParseRemote(out string) *Stats {
	s := &Stats{Time: time.Now().UTC()}
	type sample struct{ idle, total uint64 }
	s1 := map[string]sample{}
	var order []string
	s2 := map[string]sample{}
	var gpuLines strings.Builder
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, rest, _ := strings.Cut(sc.Text(), " ")
		switch key {
		case "host":
			s.Hostname = rest
		case "os":
			s.OS = strings.ToLower(rest)
		case "kernel":
			s.Kernel = rest
		case "arch":
			s.Arch = rest
		case "platform":
			s.Platform = rest
		case "uptime":
			s.Uptime, _ = strconv.ParseUint(rest, 10, 64)
		case "load":
			for _, f := range strings.Fields(rest) {
				v, _ := strconv.ParseFloat(f, 64)
				s.Load = append(s.Load, v)
			}
		case "cpumodel":
			s.CPUModel = rest
		case "s1", "s2":
			f := strings.Fields(rest)
			if len(f) < 5 {
				continue
			}
			var total, idle uint64
			for i, x := range f[1:] {
				v, _ := strconv.ParseUint(x, 10, 64)
				total += v
				if i == 3 || i == 4 { // idle + iowait
					idle += v
				}
			}
			if key == "s1" {
				s1[f[0]] = sample{idle, total}
				order = append(order, f[0])
			} else {
				s2[f[0]] = sample{idle, total}
			}
		case "mem":
			f := strings.Fields(rest)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			v *= 1024
			switch strings.TrimSuffix(f[0], ":") {
			case "MemTotal":
				s.MemTotal = v
			case "MemAvailable":
				s.MemUsed = v // fixed up below
			case "SwapTotal":
				s.SwapTotal = v
			case "SwapFree":
				s.SwapUsed = v // fixed up below
			}
		case "df":
			f := strings.Fields(rest)
			if len(f) < 6 || strings.HasPrefix(f[5], "/snap/") || strings.HasPrefix(f[5], "/boot") {
				continue
			}
			total, _ := strconv.ParseUint(f[1], 10, 64)
			used, _ := strconv.ParseUint(f[2], 10, 64)
			if total == 0 || len(s.Disks) >= 8 {
				continue
			}
			s.Disks = append(s.Disks, Disk{Mount: f[5], Total: total, Used: used})
		case "net":
			f := strings.Fields(rest)
			if len(f) == 2 {
				s.NetRx, _ = strconv.ParseUint(f[0], 10, 64)
				s.NetTx, _ = strconv.ParseUint(f[1], 10, 64)
			}
		case "gpu":
			gpuLines.WriteString(rest + "\n")
		}
	}
	if s.MemTotal > 0 && s.MemUsed <= s.MemTotal {
		s.MemUsed = s.MemTotal - s.MemUsed
	}
	if s.SwapUsed <= s.SwapTotal {
		s.SwapUsed = s.SwapTotal - s.SwapUsed
	}
	pct := func(name string) float64 {
		a, b := s1[name], s2[name]
		dt := float64(b.total - a.total)
		if b.total <= a.total || dt == 0 {
			return 0
		}
		return 100 * (dt - float64(b.idle-a.idle)) / dt
	}
	for _, name := range order {
		if name == "cpu" {
			s.CPUPercent = pct(name)
		} else {
			s.PerCore = append(s.PerCore, pct(name))
		}
	}
	s.GPUs = ParseGPUs(gpuLines.String())
	return s
}
