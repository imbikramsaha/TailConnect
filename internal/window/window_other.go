//go:build !darwin

package window

import "io"

const supported = false

// Run is unsupported off macOS (callers fall back to a browser window).
func Run(io.Reader) error { return ErrUnsupported }
