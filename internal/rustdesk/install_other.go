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
func repairUserConfig(int) bool { return false }

func stopInSession(int) {}
