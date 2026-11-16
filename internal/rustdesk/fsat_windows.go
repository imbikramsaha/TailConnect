package rustdesk

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// specDir is the folder holding the spec and, afterwards, its result.
// Windows: Lstat refuses reparse points (symlinks/junctions) for the file;
// the folder is the user's own temporary folder.
type specDir struct{ dir string }

func openSpecDir(dir string) (*specDir, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("not a folder")
	}
	return &specDir{dir}, nil
}

func (d *specDir) close() {}

// read returns the regular file name (at most max bytes) and removes it.
func (d *specDir) read(name string, max int64) ([]byte, error) {
	p := filepath.Join(d.dir, name)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, errors.New("not a small regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	f.Close()
	_ = os.Remove(p)
	return b, err
}

// create writes a new file (O_EXCL: never through something planted there).
func (d *specDir) create(name string, b []byte) error {
	f, err := os.OpenFile(filepath.Join(d.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
