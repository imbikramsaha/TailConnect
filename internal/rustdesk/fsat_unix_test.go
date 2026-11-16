//go:build !windows

package rustdesk

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSpecDirRefusesLinksAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0o700)
	os.WriteFile(filepath.Join(real, "spec.json"), []byte("{}"), 0o600)

	// A folder swapped for a link is refused.
	os.Symlink(real, filepath.Join(root, "link"))
	if _, err := openSpecDir(filepath.Join(root, "link")); err == nil {
		t.Error("linked folder opened")
	}

	d, err := openSpecDir(real)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	// A FIFO is refused without hanging; a linked file is refused.
	if err := syscall.Mkfifo(filepath.Join(real, "fifo"), 0o600); err == nil {
		if _, _, err := d.read("fifo", 4096); err == nil {
			t.Error("FIFO read")
		}
	}
	os.Symlink(filepath.Join(real, "spec.json"), filepath.Join(real, "ln.json"))
	if _, _, err := d.read("ln.json", 4096); err == nil {
		t.Error("linked file read")
	}
	os.WriteFile(filepath.Join(real, "big.json"), make([]byte, 5000), 0o600)
	if _, _, err := d.read("big.json", 4096); err == nil {
		t.Error("oversized file read")
	}
	// A regular file is read and removed.
	b, owner, err := d.read("spec.json", 4096)
	if err != nil || string(b) != "{}" || owner != os.Getuid() {
		t.Fatalf("read = %q, owner %d, %v", b, owner, err)
	}
	if _, err := os.Lstat(filepath.Join(real, "spec.json")); !os.IsNotExist(err) {
		t.Error("spec not removed")
	}
	// create never writes through an existing name or link.
	os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(real, "out.result"))
	if err := d.create("out.result", []byte("x")); err == nil {
		t.Error("wrote through a link")
	}
	if _, err := os.Lstat(filepath.Join(root, "elsewhere")); !os.IsNotExist(err) {
		t.Error("link target created")
	}
	if err := d.create("new.result", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := d.create("new.result", []byte("again")); err == nil {
		t.Error("existing result overwritten")
	}
}

// TestClearRootFilesRoot needs root (the RustDesk setup workflow runs it
// with sudo): only RustDesk's own root-owned files are removed from the
// user's folder — a hard link to another file loses only its name there,
// and links are never followed.
func TestClearRootFilesRoot(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("TAILCONNECT_ROOT_TESTS") == "" {
		t.Skip("needs root and TAILCONNECT_ROOT_TESTS=1")
	}
	const uid, gid = 4242, 4242
	root := t.TempDir()
	dir := filepath.Join(root, "prefs")
	os.Mkdir(dir, 0o755) // owned by root, like a folder 0.3.9 created
	write := func(p string) { os.WriteFile(p, []byte("x"), 0o600) }
	write(filepath.Join(dir, "RustDesk2.toml"))
	write(filepath.Join(dir, "other.txt"))
	system := filepath.Join(root, "system-file")
	write(system)
	os.Link(system, filepath.Join(dir, "RustDesk_hl.toml"))
	outside := filepath.Join(root, "outside.toml")
	write(outside)
	os.Symlink(outside, filepath.Join(dir, "RustDesk_ln.toml"))
	match := func(n string) bool { return filepath.Ext(n) == ".toml" }

	fd, err := openDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if !clearRootFiles(fd, uid, gid, match, true) {
		t.Fatal("dry run found nothing")
	}
	if _, err := os.Lstat(filepath.Join(dir, "RustDesk2.toml")); err != nil {
		t.Fatal("dry run removed a file")
	}
	if !clearRootFiles(fd, uid, gid, match, false) {
		t.Error("nothing changed")
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	owner := func(p string) uint32 {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Sys().(*syscall.Stat_t).Uid
	}
	if exists(filepath.Join(dir, "RustDesk2.toml")) || exists(filepath.Join(dir, "RustDesk_hl.toml")) {
		t.Error("root-owned RustDesk files not removed")
	}
	if !exists(filepath.Join(dir, "other.txt")) || !exists(filepath.Join(dir, "RustDesk_ln.toml")) {
		t.Error("removed a file that isn't RustDesk's or is a link")
	}
	for p, want := range map[string]uint32{dir: uid, system: 0, outside: 0} {
		if got := owner(p); got != want {
			t.Errorf("%s owned by %d, want %d", p, got, want)
		}
	}
	if b, err := os.ReadFile(system); err != nil || string(b) != "x" {
		t.Error("hard-linked file changed")
	}
	if clearRootFiles(fd, uid, gid, match, true) {
		t.Error("something left to do after clearing")
	}
	// A folder replaced by a link isn't opened.
	os.Symlink(root, filepath.Join(root, "prefs-link"))
	if _, err := openDir(filepath.Join(root, "prefs-link")); err == nil {
		t.Error("linked folder opened")
	}
}
