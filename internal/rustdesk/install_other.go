//go:build !(darwin || windows || linux)

package rustdesk

import (
	"context"
	"errors"
)

func install(context.Context, string) error {
	return errors.New("RustDesk can't be installed automatically here")
}

func startInSession(int) {}

// repairUserConfig is only needed on macOS.
func repairUserConfig(int, bool) bool { return false }

// verifyInstalled: RustDesk is installed where only administrators can
// change it.
func verifyInstalled(context.Context) error { return nil }

func stopInSession(int) {}
