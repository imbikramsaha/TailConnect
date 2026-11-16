package rustdesk

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The administrator step reads its spec from, and writes its result to, a
// folder in the user's temporary folder. It holds that folder open without
// FILE_SHARE_DELETE — so it, and the folders above it, can't be renamed,
// moved or swapped for a junction while in use — and opens files relative
// to it, never through a link.

type specDir struct{ h windows.Handle }

func openSpecDir(dir string) (*specDir, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil ||
		fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return nil, errors.New("not a plain folder")
	}
	return &specDir{h}, nil
}

func (d *specDir) close() { windows.CloseHandle(d.h) }

// openAt opens name in the folder (never through a link or reparse point).
func (d *specDir) openAt(name string, access, disposition uint32) (windows.Handle, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/:`) {
		return 0, errors.New("bad file name")
	}
	us, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{RootDirectory: d.h, ObjectName: us, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ, disposition,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	return h, err
}

// read returns the regular file name (at most max bytes) and deletes it.
// owner is -1: Windows has no numeric user IDs.
func (d *specDir) read(name string, max int64) (b []byte, owner int, err error) {
	h, err := d.openAt(name, windows.GENERIC_READ|windows.DELETE, windows.FILE_OPEN)
	if err != nil {
		return nil, -1, err
	}
	f := os.NewFile(uintptr(h), name)
	defer f.Close()
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil ||
		fi.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		fi.NumberOfLinks != 1 || fi.FileSizeHigh != 0 || int64(fi.FileSizeLow) > max {
		return nil, -1, errors.New("not a small regular file")
	}
	b, err = io.ReadAll(io.LimitReader(f, max))
	del := byte(1) // FILE_DISPOSITION_INFO{DeleteFile: TRUE}: delete this name when closed
	_ = windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, 1)
	return b, -1, err
}

// create writes a new file (never an existing one or a link).
func (d *specDir) create(name string, b []byte) error {
	h, err := d.openAt(name, windows.FILE_GENERIC_WRITE, windows.FILE_CREATE)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), name)
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// useSystemTemp makes the administrator step's own temporary files (the
// RustDesk download) go to Windows' temporary folder, which users can't
// change, instead of the user's.
func useSystemTemp() {
	if win, err := windows.GetSystemWindowsDirectory(); err == nil {
		t := filepath.Join(win, "Temp")
		os.Setenv("TMP", t)
		os.Setenv("TEMP", t)
	}
}
