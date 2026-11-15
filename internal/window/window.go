// Package window shows TailConnect's UI in a native window (WKWebView on
// macOS). It runs in its own process (`tailconnect window`) because the
// tray already owns the main AppKit run loop. URLs to show arrive on stdin,
// one per line; the window closes when stdin does.
package window

import (
	"errors"
	"net/url"
)

// ErrUnsupported means no native window on this platform; use a browser.
var ErrUnsupported = errors.New("native window not supported on this platform")

// Supported reports whether Run works here.
func Supported() bool { return supported }

// safeExternal validates URLs the page asks to open in the browser.
func safeExternal(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch u.Scheme {
	case "http", "https":
		return u.String(), true
	}
	return "", false
}
