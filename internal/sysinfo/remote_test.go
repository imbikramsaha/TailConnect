package sysinfo

import "testing"

func TestParseRemote(t *testing.T) {
	out := `host spark
os Linux
platform Ubuntu 24.04 LTS
uptime 3600
load 0.50 0.40 0.30
s1 cpu  100 0 100 800 0 0 0 0
s1 cpu0 50 0 50 400 0 0 0 0
s2 cpu  150 0 150 900 0 0 0 0
s2 cpu0 100 0 50 450 0 0 0 0
mem MemTotal: 1000 kB
mem MemAvailable: 250 kB
mem SwapTotal: 0 kB
mem SwapFree: 0 kB
df /dev/nvme0n1p2 1000 400 600 40% /
net 1000 2000
gpu NVIDIA GB10, 37, [N/A], [N/A], 45, 30.5
`
	s := ParseRemote(out)
	if s.Hostname != "spark" || s.OS != "linux" || s.Uptime != 3600 || len(s.Load) != 3 {
		t.Errorf("basics: %+v", s)
	}
	if s.CPUPercent != 50 || len(s.PerCore) != 1 || s.PerCore[0] != 50 {
		t.Errorf("cpu = %v %v", s.CPUPercent, s.PerCore)
	}
	if s.MemTotal != 1000*1024 || s.MemUsed != 750*1024 {
		t.Errorf("mem = %d/%d", s.MemUsed, s.MemTotal)
	}
	if len(s.Disks) != 1 || s.Disks[0].Used != 400 {
		t.Errorf("disks = %+v", s.Disks)
	}
	if len(s.GPUs) != 1 || s.GPUs[0].Util != 37 || s.GPUs[0].MemTotal != -1 || s.GPUs[0].PowerW != 30.5 {
		t.Errorf("gpus = %+v", s.GPUs)
	}
}
