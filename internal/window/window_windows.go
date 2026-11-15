package window

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"

	"github.com/imbikramsaha/tailconnect/internal/launch"
)

// supported reports whether the Edge WebView2 runtime is installed (it ships
// with Windows 11 and current Windows 10).
var supported = func() bool {
	v, err := webviewloader.GetInstalledVersion()
	return err == nil && v != ""
}()

var (
	user32                   = syscall.NewLazyDLL("user32.dll")
	setForegroundWnd         = user32.NewProc("SetForegroundWindow")
	showWindow               = user32.NewProc("ShowWindow")
	swRestore        uintptr = 9
)

func activate(hwnd uintptr) {
	showWindow.Call(hwnd, swRestore)
	setForegroundWnd.Call(hwnd)
}

// Run shows the window, navigating to each URL read from in.
func Run(in io.Reader) error {
	if !supported {
		return ErrUnsupported
	}
	lines := bufio.NewScanner(in)
	if !lines.Scan() {
		return nil
	}
	first := strings.TrimSpace(lines.Text())
	data := filepath.Join(os.Getenv("LOCALAPPDATA"), "TailConnect", "WebView2")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  data,
		WindowOptions: webview2.WindowOptions{
			Title: "TailConnect", Width: 1180, Height: 800, Center: true, IconId: 1,
		},
	})
	if w == nil {
		return ErrUnsupported
	}
	defer w.Destroy()
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
				activate(uintptr(w.Window()))
			})
		}
		w.Dispatch(w.Terminate)
	}()
	w.Run()
	return nil
}
