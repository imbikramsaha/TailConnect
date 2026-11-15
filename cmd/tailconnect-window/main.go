// Command tailconnect-window is TailConnect's native window on Linux
// (GTK + WebKitGTK 4.1). The app starts it and sends URLs on stdin.
// Build with: CGO_ENABLED=1 go build -tags webkit ./cmd/tailconnect-window
package main

import (
	"fmt"
	"os"

	"github.com/imbikramsaha/tailconnect/internal/window"
)

func main() {
	if err := window.Run(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "tailconnect-window:", err)
		os.Exit(1)
	}
}
