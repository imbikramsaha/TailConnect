package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
void tcInstallReopenHandler(void);
*/
import "C"

var reopenFn func()

//export tcReopen
func tcReopen() {
	if fn := reopenFn; fn != nil {
		go fn()
	}
}

// onReopen runs fn when the user opens the app again while it is running
// (double-click in Finder/Launchpad/Spotlight), which macOS reports as a
// "reopen" Apple event rather than a new launch.
func onReopen(fn func()) {
	reopenFn = fn
	C.tcInstallReopenHandler()
}
