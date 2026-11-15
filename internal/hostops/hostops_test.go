package hostops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLsofAndFilter(t *testing.T) {
	out := "p100\ncjupyter-lab\nn127.0.0.1:8888\nn[::1]:8888\np200\ncrapportd\nn*:49152\np300\ncollama\nn127.0.0.1:11434\np400\ncpostgres\nn*:5432\np500\ncnginx\nn192.168.1.5:8080\n"
	got := finish(ParseLsof(out), -1)
	ports := map[int]string{}
	for _, s := range got {
		ports[s.Port] = s.Label
	}
	if ports[8888] != "Jupyter" || ports[11434] != "Ollama API" || ports[5432] != "PostgreSQL" {
		t.Errorf("labels: %v", ports)
	}
	if _, ok := ports[49152]; ok {
		t.Error("noise/ephemeral port not filtered")
	}
	if _, ok := ports[8080]; ok {
		t.Error("LAN-only bind should be unreachable through a loopback tunnel")
	}
	if !got[0].Web || got[len(got)-1].Web {
		t.Errorf("web services should sort first: %+v", got)
	}
}

func TestParseRemoteServicesSS(t *testing.T) {
	out := `ss LISTEN 0 4096 127.0.0.1:8188 0.0.0.0:* users:(("python3",pid=4242,fd=3))
ss LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=1,fd=3))
ss LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*
ss LISTEN 0 511 [::]:3000 [::]:* users:(("node",pid=77,fd=20))`
	got := ParseRemoteServices(out)
	m := map[int]string{}
	for _, s := range got {
		m[s.Port] = s.Process
	}
	if m[8188] != "python3" || m[22] != "sshd" || m[3000] != "node" {
		t.Errorf("parsed %+v", got)
	}
	if _, ok := m[53]; ok {
		t.Error("service bound to 127.0.0.53 is unreachable via a 127.0.0.1 tunnel and must be skipped")
	}
}

func TestContainersAndProcs(t *testing.T) {
	c := ParseContainers(`{"ID":"abc123","Names":"webui","Image":"ghcr.io/open-webui/open-webui:main","State":"running","Status":"Up 2 hours","Ports":"127.0.0.1:3000->8080/tcp"}` + "\nnot json\n")
	if len(c) != 1 || c[0].Name != "webui" || c[0].State != "running" {
		t.Errorf("containers %+v", c)
	}
	for _, bad := range [][2]string{{"x;rm -rf", "stop"}, {"ok", "rm"}, {"", "start"}} {
		if ValidContainerAction(bad[0], bad[1]) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	p := ParseRemoteProcs("  123 alice 95.0  2.5 102400 /usr/bin/python3\n  7 root 0.0 0.1 2048 sshd\ngpu 123, 2048\n")
	if len(p) != 2 || p[0].GPUMiB != 2048 || p[0].Name != "python3" || p[0].RSS != 102400*1024 {
		t.Errorf("procs %+v", p)
	}
}

func TestMagicPacket(t *testing.T) {
	p, err := MagicPacket("9c:58:84:04:f9:27")
	if err != nil || len(p) != 102 || p[0] != 0xff || p[6] != 0x9c || p[101] != 0x27 {
		t.Fatalf("packet %x %v", p, err)
	}
	if _, err := MagicPacket("nope"); err == nil {
		t.Error("bad MAC accepted")
	}
}

func TestReceiveSafeNames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"../../etc/passwd", "a.txt", "a.txt", `..\\evil.bat`, ".hidden"} {
		p, _, err := Receive(dir, n, strings.NewReader("x"))
		if err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		if filepath.Dir(p) != dir {
			t.Errorf("%q escaped to %s", n, p)
		}
	}
	for _, want := range []string{"passwd", "a.txt", "a (1).txt", "evil.bat", "hidden"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	if _, err := SafeName(".."); err == nil {
		t.Error(`".." accepted`)
	}
}
