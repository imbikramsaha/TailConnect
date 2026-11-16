package hostops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/launch"
)

const maxProcs = 40

// Processes returns the busiest processes (by CPU, then memory), with GPU
// memory for NVIDIA compute processes.
func Processes(ctx context.Context) ([]agent.Process, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	type sample struct {
		p   *process.Process
		cpu float64
	}
	first := make(map[int32]float64, len(procs))
	for _, p := range procs {
		if t, err := p.TimesWithContext(ctx); err == nil {
			first[p.Pid] = t.User + t.System
		}
	}
	const interval = 600 * time.Millisecond
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(interval):
	}
	var samples []sample
	for _, p := range procs {
		t0, ok := first[p.Pid]
		if !ok {
			continue
		}
		t, err := p.TimesWithContext(ctx)
		if err != nil {
			continue
		}
		samples = append(samples, sample{p, 100 * (t.User + t.System - t0) / interval.Seconds()})
	}
	var total uint64
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		total = vm.Total
	}
	gpu := gpuProcesses(ctx)
	out := make([]agent.Process, 0, len(samples))
	for _, s := range samples {
		pr := agent.Process{PID: int(s.p.Pid), CPU: s.cpu, GPUMiB: gpu[int(s.p.Pid)]}
		if mi, err := s.p.MemoryInfoWithContext(ctx); err == nil {
			pr.RSS = mi.RSS
			if total > 0 {
				pr.Mem = 100 * float64(mi.RSS) / float64(total)
			}
		}
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.GPUMiB > 0) != (b.GPUMiB > 0) {
			return a.GPUMiB > 0
		}
		if int(a.CPU) != int(b.CPU) {
			return a.CPU > b.CPU
		}
		return a.RSS > b.RSS
	})
	if len(out) > maxProcs {
		out = out[:maxProcs]
	}
	// Names/users/cmds only for the rows we return (they are slow to read).
	for i := range out {
		p, err := process.NewProcessWithContext(ctx, int32(out[i].PID))
		if err != nil {
			continue
		}
		out[i].Name, _ = p.NameWithContext(ctx)
		out[i].User, _ = p.UsernameWithContext(ctx)
		if c, err := p.CmdlineWithContext(ctx); err == nil {
			if len(c) > 300 {
				c = c[:300] + "…"
			}
			out[i].Cmd = c
		}
	}
	return out, nil
}

// gpuProcesses maps PID → GPU memory (MiB) for NVIDIA compute processes.
func gpuProcesses(ctx context.Context) map[int]float64 {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	return ParseGPUProcs(string(out))
}

// ParseGPUProcs parses `nvidia-smi --query-compute-apps=pid,used_memory` CSV.
func ParseGPUProcs(out string) map[int]float64 {
	m := map[int]float64{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(strings.TrimSpace(f[0]))
		v, err2 := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
		if err1 == nil && err2 == nil {
			m[pid] += v
		}
	}
	return m
}

// Kill terminates a process owned by the user running TailConnect.
func Kill(ctx context.Context, pid int) error {
	// Bound-check before narrowing to int32: a huge value would otherwise
	// wrap around to a different process ID.
	if pid <= 1 || pid > math.MaxInt32 || pid == os.Getpid() {
		return errors.New("refusing to stop that process")
	}
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return fmt.Errorf("no process %d", pid)
	}
	owner, err := p.UsernameWithContext(ctx)
	u, uerr := user.Current()
	if err != nil || uerr != nil || owner == "" {
		return fmt.Errorf("can't tell who owns process %d; not stopping it", pid)
	}
	if !sameUser(owner, u.Username) {
		return fmt.Errorf("process %d belongs to %s; only your own processes can be stopped", pid, owner)
	}
	if runtime.GOOS == "windows" {
		return p.KillWithContext(ctx)
	}
	return p.TerminateWithContext(ctx)
}

func sameUser(a, b string) bool {
	trim := func(s string) string {
		if i := strings.LastIndexAny(s, `\`); i >= 0 {
			s = s[i+1:]
		}
		return strings.ToLower(s)
	}
	return trim(a) == trim(b)
}

// RemoteProcsCmd lists the busiest processes on an SSH-only host.
const RemoteProcsCmd = `LC_ALL=C ps -axo pid=,user=,pcpu=,pmem=,rss=,comm= 2>/dev/null | sort -k3 -nr | head -40; ` +
	`command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi --query-compute-apps=pid,used_memory --format=csv,noheader,nounits 2>/dev/null | sed 's/^/gpu /'; true`

// ParseRemoteProcs parses RemoteProcsCmd output.
func ParseRemoteProcs(out string) []agent.Process {
	var procs []agent.Process
	var gpu strings.Builder
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "gpu "); ok {
			gpu.WriteString(rest + "\n")
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		memp, _ := strconv.ParseFloat(f[3], 64)
		rss, _ := strconv.ParseUint(f[4], 10, 64)
		cmd := strings.Join(f[5:], " ")
		procs = append(procs, agent.Process{PID: pid, User: f[1], CPU: cpu, Mem: memp, RSS: rss * 1024,
			Name: filepath.Base(cmd), Cmd: cmd})
	}
	g := ParseGPUProcs(gpu.String())
	for i := range procs {
		procs[i].GPUMiB = g[procs[i].PID]
	}
	return procs
}

// ---- Docker ----

// dockerBin finds docker in PATH or the usual install locations. Paths
// outside PATH are only used if no other local user could have planted
// them (on a shared Mac, /opt/homebrew may belong to someone else).
func dockerBin() (string, error) {
	if p, err := launch.LookPath("docker"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/Applications/Docker.app/Contents/Resources/bin/docker", "/usr/bin/docker"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && launch.TrustedFile(p) {
			return p, nil
		}
	}
	return "", errors.New("Docker is not installed")
}

// DockerPSCmd lists containers (shared by local and SSH hosts).
const DockerPSCmd = `docker ps -a --format '{{json .}}'`

// Containers lists Docker containers ("" error-free empty list if Docker is
// absent is signalled by an error).
func Containers(ctx context.Context) ([]agent.Container, error) {
	bin, err := dockerBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "ps", "-a", "--format", "{{json .}}").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("docker: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("docker: %w", err)
	}
	return ParseContainers(string(out)), nil
}

// ParseContainers parses `docker ps --format '{{json .}}'` output.
func ParseContainers(out string) []agent.Container {
	var res []agent.Container
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r struct{ ID, Names, Image, State, Status, Ports string }
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		res = append(res, agent.Container{ID: r.ID, Name: r.Names, Image: r.Image, State: r.State, Status: r.Status, Ports: r.Ports})
	}
	return res
}

var containerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ValidContainerAction validates a container action request.
func ValidContainerAction(id, action string) error {
	if !containerRef.MatchString(id) {
		return errors.New("invalid container id")
	}
	switch action {
	case "start", "stop", "restart":
		return nil
	}
	return errors.New("action must be start, stop or restart")
}

// ContainerAction starts, stops or restarts a container.
func ContainerAction(ctx context.Context, id, action string) error {
	if err := ValidContainerAction(id, action); err != nil {
		return err
	}
	bin, err := dockerBin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, action, id).CombinedOutput(); err != nil {
		return fmt.Errorf("docker %s: %s", action, strings.TrimSpace(string(out)))
	}
	return nil
}
