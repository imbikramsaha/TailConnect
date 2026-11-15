package core

import (
	"testing"

	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/sysinfo"
)

func TestBroadcastOf(t *testing.T) {
	for in, want := range map[string]string{"192.168.0.0/24": "192.168.0.255", "10.0.0.0/8": "10.255.255.255", "172.16.4.0/22": "172.16.7.255"} {
		if got := broadcastOf(in); got != want {
			t.Errorf("broadcastOf(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestCheckAlerts(t *testing.T) {
	s := &sysinfo.Stats{MemTotal: 100, MemUsed: 97,
		Disks: []sysinfo.Disk{{Mount: "/", Total: 100, Used: 50}, {Mount: "/data", Total: 100, Used: 99}},
		GPUs:  []sysinfo.GPU{{Name: "GB10", TempC: 91}}}
	got := CheckAlerts(s, store.DefaultAlerts())
	keys := map[string]bool{}
	for _, a := range got {
		keys[a.Key] = true
	}
	if !keys["mem"] || !keys["disk:/data"] || keys["disk:/"] || !keys["gpu0"] {
		t.Errorf("alerts %+v", got)
	}
}

func TestPairCodeStable(t *testing.T) {
	a := PairCode("n1", "ssh-ed25519 AAAA", "n2")
	if a != PairCode("n1", "ssh-ed25519 AAAA", "n2") || a == PairCode("n1", "ssh-ed25519 AAAB", "n2") || len(a) != 7 {
		t.Errorf("code %q", a)
	}
}
