package window

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
void tcWindowSetup(void *win);
void tcWindowActivate(void);
*/
import "C"

import (
	"bufio"
	"io"
	"strings"

	webview "github.com/webview/webview_go"

	"github.com/imbikramsaha/tailconnect/internal/launch"
)

const supported = true

// Run shows the window, navigating to each URL read from in. It must be
// called on the main goroutine (webview locks it to the main thread).
func Run(in io.Reader) error {
	lines := bufio.NewScanner(in)
	if !lines.Scan() {
		return nil
	}
	first := strings.TrimSpace(lines.Text())
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("TailConnect")
	w.SetSize(1180, 800, webview.HintNone)
	C.tcWindowSetup(w.Window())
	_ = w.Bind("tcOpenExternal", func(raw string) {
		if u, ok := safeExternal(raw); ok {
			_ = launch.OpenURL(u)
		}
	})
	w.Navigate(first)
	go func() {
		for lines.Scan() {
			u := strings.TrimSpace(lines.Text())
			if u == "" {
				continue
			}
			w.Dispatch(func() {
				w.Navigate(u)
				C.tcWindowActivate()
			})
		}
		// The app quit (stdin closed): close the window too.
		w.Dispatch(w.Terminate)
	}()
	w.Run()
	return nil
}
