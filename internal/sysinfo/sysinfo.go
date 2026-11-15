// Package sysinfo collects resource-monitor data: CPU (per core), memory,
// disks, network totals and NVIDIA GPUs (via nvidia-smi when present).
package sysinfo

import (
	"bufio"
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// Stats is one resource snapshot.
type Stats struct {
	Time       time.Time `json:"time"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	Platform   string    `json:"platform"`
	Kernel     string    `json:"kernel,omitempty"`
	Arch       string    `json:"arch,omitempty"`
	Uptime     uint64    `json:"uptime"`
	CPUModel   string    `json:"cpuModel,omitempty"`
	CPUPercent float64   `json:"cpuPercent"`
	PerCore    []float64 `json:"perCore"`
	Load       []float64 `json:"load,omitempty"`
	MemTotal   uint64    `json:"memTotal"`
	MemUsed    uint64    `json:"memUsed"`
	SwapTotal  uint64    `json:"swapTotal"`
	SwapUsed   uint64    `json:"swapUsed"`
	Disks      []Disk    `json:"disks"`
	NetRx      uint64    `json:"netRx"`
	NetTx      uint64    `json:"netTx"`
	GPUs       []GPU     `json:"gpus"`
}

// Disk is one mounted filesystem.
type Disk struct {
	Mount string `json:"mount"`
	FS    string `json:"fs,omitempty"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// GPU is one NVIDIA GPU. Negative values mean "not reported" (e.g. unified
// memory systems report no dedicated VRAM).
type GPU struct {
	Name     string  `json:"name"`
	Util     float64 `json:"util"`
	MemTotal float64 `json:"memTotalMiB"`
	MemUsed  float64 `json:"memUsedMiB"`
	TempC    float64 `json:"tempC"`
	PowerW   float64 `json:"powerW"`
}

var skipFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true, "proc": true,
	"sysfs": true, "devfs": true, "autofs": true, "nullfs": true, "efivarfs": true, "ramfs": true,
}

// Collect gathers a snapshot. It takes ~sample to measure CPU usage.
func Collect(ctx context.Context, sample time.Duration) (*Stats, error) {
	s := &Stats{Time: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if hi, err := host.InfoWithContext(ctx); err == nil {
		s.Hostname = hi.Hostname
		s.Platform = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
		s.Kernel = hi.KernelVersion
		s.Uptime = hi.Uptime
	}
	if per, err := cpu.PercentWithContext(ctx, sample, true); err == nil {
		s.PerCore = per
		var sum float64
		for _, p := range per {
			sum += p
		}
		if len(per) > 0 {
			s.CPUPercent = sum / float64(len(per))
		}
	}
	if ci, err := cpu.InfoWithContext(ctx); err == nil && len(ci) > 0 {
		s.CPUModel = strings.TrimSpace(ci[0].ModelName)
	}
	if runtime.GOOS != "windows" {
		if la, err := load.AvgWithContext(ctx); err == nil {
			s.Load = []float64{la.Load1, la.Load5, la.Load15}
		}
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		s.MemTotal = vm.Total
		s.MemUsed = vm.Total - vm.Available
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.SwapTotal, s.SwapUsed = sw.Total, sw.Used
	}
	if parts, err := disk.PartitionsWithContext(ctx, false); err == nil {
		seen := map[string]bool{}
		for _, p := range parts {
			if skipFS[p.Fstype] || seen[p.Device] || strings.HasPrefix(p.Mountpoint, "/snap/") ||
				strings.HasPrefix(p.Mountpoint, "/boot/efi") || strings.HasPrefix(p.Mountpoint, "/System/Volumes/") && p.Mountpoint != "/System/Volumes/Data" {
				continue
			}
			u, err := disk.UsageWithContext(ctx, p.Mountpoint)
			if err != nil || u.Total == 0 {
				continue
			}
			seen[p.Device] = true
			s.Disks = append(s.Disks, Disk{Mount: p.Mountpoint, FS: p.Fstype, Total: u.Total, Used: u.Used})
			if len(s.Disks) >= 8 {
				break
			}
		}
	}
	if io, err := gnet.IOCountersWithContext(ctx, false); err == nil && len(io) > 0 {
		s.NetRx, s.NetTx = io[0].BytesRecv, io[0].BytesSent
	}
	s.GPUs = nvidiaGPUs(ctx)
	return s, nil
}

// GPUQuery is the nvidia-smi invocation shared with remote collection.
const GPUQuery = "nvidia-smi --query-gpu=name,utilization.gpu,memory.total,memory.used,temperature.gpu,power.draw --format=csv,noheader,nounits"

func nvidiaGPUs(ctx context.Context) []GPU {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	args := strings.Fields(GPUQuery)[1:]
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return nil
	}
	return ParseGPUs(string(out))
}

// ParseGPUs parses nvidia-smi CSV output (see GPUQuery).
func ParseGPUs(out string) []GPU {
	var gpus []GPU
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 6 {
			continue
		}
		num := func(s string) float64 {
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return -1
			}
			return v
		}
		gpus = append(gpus, GPU{
			Name: strings.TrimSpace(f[0]), Util: num(f[1]), MemTotal: num(f[2]),
			MemUsed: num(f[3]), TempC: num(f[4]), PowerW: num(f[5]),
		})
	}
	return gpus
}
