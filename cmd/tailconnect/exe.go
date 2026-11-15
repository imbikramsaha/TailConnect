package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Windows ships two builds of this program: TailConnectApp.exe (GUI
// subsystem: tray, window, autostart — never flashes a console) and
// tailconnect.exe (console subsystem: CLI and ssh ProxyCommand).
const (
	winAppName = "TailConnectApp.exe"
	winCLIName = "tailconnect.exe"
)

func sibling(name string) (string, bool) {
	p := filepath.Join(filepath.Dir(executable()), name)
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return "", false
}

// appExe is the binary that runs the app (tray, window, autostart).
func appExe() string {
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Base(executable()), winAppName) {
		if p, ok := sibling(winAppName); ok {
			return p
		}
	}
	return executable()
}

// cliExe is the binary for the command line and ssh ProxyCommand.
func cliExe() string {
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Base(executable()), winCLIName) {
		if p, ok := sibling(winCLIName); ok {
			return p
		}
	}
	return executable()
}
