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

// read returns the regular file name (at most max bytes) and removes it.
func (d *specDir) read(name string, max int64) ([]byte, error) {
	// O_NONBLOCK: a planted FIFO mustn't hang the open (Fstat rejects it).
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, errors.New("not a small regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	_ = unix.Unlinkat(d.fd, name, 0)
	return b, err
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

// giveBack hands the folder open as dfd, and the regular files in it whose
// names match, from root back to uid:gid. Each file is opened without
// following links, and skipped if it has another name: a hard link the
// user planted could be a system file.
func giveBack(dfd, uid, gid int, match func(name string) bool) (changed bool) {
	var st unix.Stat_t
	if unix.Fstat(dfd, &st) == nil && st.Uid == 0 && unix.Fchown(dfd, uid, gid) == nil {
		changed = true
	}
	names, err := dirNames(dfd)
	if err != nil {
		return changed
	}
	for _, n := range names {
		if !match(n) {
			continue
		}
		if unix.Fstatat(dfd, n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		fd, err := unix.Openat(dfd, n, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		if unix.Fstat(fd, &st) == nil && st.Uid == 0 && st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink == 1 &&
			unix.Fchown(fd, uid, gid) == nil {
			changed = true
		}
		unix.Close(fd)
	}
	return changed
}
