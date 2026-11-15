//go:build linux && cgo && webkit

package window

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <stdlib.h>
void tcLinuxInit(const char *url);
void tcLinuxNavigate(const char *url);
void tcLinuxQuit(void);
void tcLinuxRun(void);
*/
import "C"

import (
	"bufio"
	"io"
	"runtime"
	"strings"
	"unsafe"

	"github.com/imbikramsaha/tailconnect/internal/launch"
)

// GTK must run on the main OS thread.
func init() { runtime.LockOSThread() }

const supported = true

//export tcOpenExternal
func tcOpenExternal(raw *C.char) {
	if u, ok := safeExternal(C.GoString(raw)); ok {
		go launch.OpenURL(u)
	}
}

// Run shows the window, navigating to each URL read from in.
func Run(in io.Reader) error {
	lines := bufio.NewScanner(in)
	if !lines.Scan() {
		return nil
	}
	first := C.CString(strings.TrimSpace(lines.Text()))
	C.tcLinuxInit(first)
	C.free(unsafe.Pointer(first))
	go func() {
		for lines.Scan() {
			if u := strings.TrimSpace(lines.Text()); u != "" {
				cu := C.CString(u)
				C.tcLinuxNavigate(cu)
				C.free(unsafe.Pointer(cu))
			}
		}
		C.tcLinuxQuit()
	}()
	C.tcLinuxRun()
	return nil
}
