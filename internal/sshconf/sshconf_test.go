package sshconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlias(t *testing.T) {
	for in, want := range map[string]string{
		"Bikram’s MacBook Air": "tc-bikrams-macbook-air",
		"GPU server #2":        "tc-gpu-server-2",
		"   ":                  "tc-device",
		"DESKTOP-AB12":         "tc-desktop-ab12",
	} {
		if got := Alias(in); got != want {
			t.Errorf("Alias(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIncludeIsIdempotentAndRemovable(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config")
	managed := filepath.Join(dir, "tailconnect", "config")
	orig := "Host work\n  HostName work.example.com\n"
	os.WriteFile(user, []byte(orig), 0o600)
	for i := 0; i < 2; i++ {
		if err := EnsureInclude(user, managed); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(user)
	if strings.Count(string(b), IncludeLine(managed)) != 1 || !strings.HasPrefix(string(b), "# Added by TailConnect") {
		t.Fatalf("include not prepended once:\n%s", b)
	}
	if bak, _ := os.ReadFile(user + ".tailconnect.bak"); string(bak) != orig {
		t.Errorf("backup = %q", bak)
	}
	if err := RemoveInclude(user, managed); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(user); string(b) != orig {
		t.Errorf("after remove:\n%q\nwant\n%q", b, orig)
	}
}

func TestRenderProxy(t *testing.T) {
	out := Render([]Entry{{Alias: "tc-a", User: "me", Port: 22, DeviceID: "p-1", Proxy: true}},
		Options{Exe: "/Applications/TailConnect.app/Contents/MacOS/tailconnect", KeyPath: "/k", KnownHosts: "/kh"})
	for _, want := range []string{"Host tc-a", "User me", "HostKeyAlias tc-a",
		`ProxyCommand "/Applications/TailConnect.app/Contents/MacOS/tailconnect" proxy p-1 %p`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
