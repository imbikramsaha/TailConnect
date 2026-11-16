package rustdesk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		p := NewPassword()
		if !passwordRe.MatchString(p) {
			t.Fatalf("password %q doesn't match the allowed format", p)
		}
		if seen[p] {
			t.Fatal("repeated password")
		}
		seen[p] = true
	}
}

func TestAssetName(t *testing.T) {
	n, err := assetName("1.5.3")
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		if err == nil {
			t.Fatal("expected unsupported architecture")
		}
		return
	}
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Skip(err) // no dpkg/rpm on this machine
		}
		t.Fatal(err)
	}
	if !strings.HasPrefix(n, "rustdesk-1.5.3-") {
		t.Fatalf("asset %q", n)
	}
	for _, bad := range []string{"", "1.5.3/../x", "1 5"} {
		if _, err := assetName(bad); err == nil {
			t.Errorf("version %q accepted", bad)
		}
	}
}

func TestAdminMainRefusesBadSpecs(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, v any) string {
		p := filepath.Join(dir, name)
		b, _ := json.Marshal(v)
		os.WriteFile(p, b, 0o600)
		return p
	}
	if err := AdminMain("relative/spec.json"); err == nil {
		t.Error("relative path accepted")
	}
	if err := AdminMain(write("short.json", Spec{Password: "short"})); err == nil {
		t.Error("weak password accepted")
	}
	if err := AdminMain(write("dash.json", Spec{Password: "--option-whitelist-x"})); err == nil {
		t.Error("password that looks like an option accepted")
	}
	if err := AdminMain(write("uid.json", Spec{UID: -1})); err == nil {
		t.Error("negative uid accepted")
	}
	if runtime.GOOS != "windows" {
		if err := AdminMain(write("other-uid.json", Spec{UID: os.Getuid() + 1})); err == nil {
			t.Error("uid other than the spec file's owner accepted")
		}
	}
	if runtime.GOOS != "windows" {
		target := write("real.json", Spec{})
		link := filepath.Join(dir, "link.json")
		os.Symlink(target, link)
		if err := AdminMain(link); err == nil {
			t.Error("symlinked spec followed")
		}
	}
}
