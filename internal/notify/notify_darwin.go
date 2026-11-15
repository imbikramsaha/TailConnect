package notify

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#include <stdlib.h>
int tcNotifyAvailable(void);
void tcNotify(const char *title, const char *body, const char *route);
*/
import "C"

import (
	"os/exec"
	"strings"
	"unsafe"
)

//export tcNotifyClicked
func tcNotifyClicked(route *C.char) { clicked(C.GoString(route)) }

func send(title, body, route string) error {
	title, body = clean(title), clean(body)
	if C.tcNotifyAvailable() == 1 {
		ct, cb, cr := C.CString(title), C.CString(body), C.CString(route)
		defer C.free(unsafe.Pointer(ct))
		defer C.free(unsafe.Pointer(cb))
		defer C.free(unsafe.Pointer(cr))
		C.tcNotify(ct, cb, cr)
		return nil
	}
	// Not running from an app bundle (dev builds): AppleScript fallback.
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	return exec.Command("osascript", "-e", "display notification "+q(body)+" with title "+q(title)).Run()
}
