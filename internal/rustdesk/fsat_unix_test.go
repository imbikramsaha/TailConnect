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
		if _, err := d.read("fifo", 4096); err == nil {
			t.Error("FIFO read")
		}
	}
	os.Symlink(filepath.Join(real, "spec.json"), filepath.Join(real, "ln.json"))
	if _, err := d.read("ln.json", 4096); err == nil {
		t.Error("linked file read")
	}
	os.WriteFile(filepath.Join(real, "big.json"), make([]byte, 5000), 0o600)
	if _, err := d.read("big.json", 4096); err == nil {
		t.Error("oversized file read")
	}
	// A regular file is read and removed.
	b, err := d.read("spec.json", 4096)
	if err != nil || string(b) != "{}" {
		t.Fatalf("read = %q, %v", b, err)
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

// TestGiveBackRoot needs root (the RustDesk setup workflow runs it with
// sudo): it checks that only RustDesk's own root-owned files are handed to
// the user — never a hard link to another file, never through a link.
func TestGiveBackRoot(t *testing.T) {
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
	secret := filepath.Join(root, "system-file")
	write(secret)
	os.Link(secret, filepath.Join(dir, "RustDesk_hl.toml"))
	outside := filepath.Join(root, "outside.toml")
	write(outside)
	os.Symlink(outside, filepath.Join(dir, "RustDesk_ln.toml"))

	fd, err := openDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed := giveBack(fd, uid, gid, func(n string) bool { return filepath.Ext(n) == ".toml" })
	syscall.Close(fd)
	if !changed {
		t.Error("nothing changed")
	}
	owner := func(p string) uint32 {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Sys().(*syscall.Stat_t).Uid
	}
	for p, want := range map[string]uint32{
		dir:                                  uid,
		filepath.Join(dir, "RustDesk2.toml"): uid,
		filepath.Join(dir, "other.txt"):      0, // not RustDesk's
		secret:                               0, // hard-linked in: skipped
		outside:                              0, // symlinked in: not followed
	} {
		if got := owner(p); got != want {
			t.Errorf("%s owned by %d, want %d", p, got, want)
		}
	}
	// A folder replaced by a link isn't opened.
	os.Symlink(root, filepath.Join(root, "prefs-link"))
	if _, err := openDir(filepath.Join(root, "prefs-link")); err == nil {
		t.Error("linked folder opened")
	}
}
