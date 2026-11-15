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

func TestRenderRejectsInjection(t *testing.T) {
	o := Options{Exe: "/bin/tailconnect", KeyPath: "/k", KnownHosts: "/kh"}
	evil := []Entry{
		{Alias: "tc-a", User: "bob\nHost *\n  ProxyCommand touch /tmp/pwned", Port: 22, DeviceID: "p-1", Proxy: true},
		{Alias: "tc-b", HostName: "x\nProxyCommand evil", Port: 22},
		{Alias: "tc-c\nHost *", Port: 22},
		{Alias: "tc-d", User: "%d", Port: 22},
		{Alias: "tc-e", User: "-oProxyCommand=evil", Port: 22},
		{Alias: "tc-f", DeviceID: "p-1 ; evil", Proxy: true, Port: 22},
	}
	out := Render(evil, o)
	for _, bad := range []string{"pwned", "evil", "Host *", "%d"} {
		if strings.Contains(out, bad) {
			t.Errorf("injected %q into config:\n%s", bad, out)
		}
	}
	ok := Render([]Entry{{Alias: "tc-win", User: "John Smith", Port: 22, HostName: "10.0.0.5"}}, o)
	if !strings.Contains(ok, `User "John Smith"`) {
		t.Errorf("user with space not quoted:\n%s", ok)
	}
	if out := Render(nil, Options{Exe: "/a\"b", KeyPath: "/k", KnownHosts: "/kh"}); strings.Contains(out, "Host ") {
		t.Errorf("rendered with unsafe exe path:\n%s", out)
	}
}
