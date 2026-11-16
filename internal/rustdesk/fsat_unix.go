//go:build !windows

package rustdesk

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// The administrator step works in folders the user controls (the spec's
// temporary folder, the user's RustDesk settings). Every name is opened
// relative to a folder that is already open and never through a link, so
// swapping a folder or file for a link can't send it anywhere else.

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openDir opens path, then each name below it in turn, as folders, without
// following links. The caller closes the result with unix.Close.
func openDir(path string, names ...string) (int, error) {
	fd, err := unix.Open(path, dirFlags, 0)
	if err != nil {
		return -1, err
	}
	for _, n := range names {
		next, err := unix.Openat(fd, n, dirFlags, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// dirNames lists the folder open as dfd.
func dirNames(dfd int) ([]string, error) {
	nfd, err := unix.Dup(dfd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(nfd), "dir")
	defer f.Close()
	return f.Readdirnames(-1)
}

// specDir is the folder holding the spec and, afterwards, its result.
type specDir struct{ fd int }

func openSpecDir(dir string) (*specDir, error) {
	fd, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	return &specDir{fd}, nil
}

func (d *specDir) close() { unix.Close(d.fd) }

// read returns the regular file name (at most max bytes) and its owner's
// user ID, and removes it.
func (d *specDir) read(name string, max int64) (b []byte, owner int, err error) {
	// O_NONBLOCK: a planted FIFO mustn't hang the open (Fstat rejects it).
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, -1, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > max {
		return nil, -1, errors.New("not a small regular file")
	}
	b, err = io.ReadAll(io.LimitReader(f, max))
	_ = unix.Unlinkat(d.fd, name, 0)
	return b, int(st.Uid), err
}

// create writes a new file (never an existing one, never through a link).
func (d *specDir) create(name string, b []byte) error {
	fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// clearRootFiles gives the folder open as dfd back to uid:gid if root owns
// it, and removes the regular files in it owned by root whose names match:
// files an earlier administrator run left in the user's folder, which the
// user's RustDesk can't read (setup writes the settings again afterwards).
// Removing a name in the user's own folder never changes any other file,
// even one it's a hard link to, and links are never followed. With dryRun
// it only reports whether there's anything to do.
func clearRootFiles(dfd, uid, gid int, match func(name string) bool, dryRun bool) (found bool) {
	var st unix.Stat_t
	if unix.Fstat(dfd, &st) == nil && st.Uid == 0 {
		found = true
		if !dryRun {
			_ = unix.Fchown(dfd, uid, gid) // a folder: can't be a hard link
		}
	}
	names, err := dirNames(dfd)
	if err != nil {
		return found
	}
	for _, n := range names {
		if !match(n) || unix.Fstatat(dfd, n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		found = true
		if !dryRun {
			_ = unix.Unlinkat(dfd, n, 0)
		}
	}
	return found
}

// useSystemTemp makes the administrator step's own temporary folders (the
// RustDesk download, RustDesk's private HOME) go to /tmp, where only their
// owner can rename or replace them — not to the user's temporary folder,
// which the elevation may pass on in TMPDIR.
func useSystemTemp() { os.Unsetenv("TMPDIR") }
