//go:build !(darwin || windows || linux)

package rustdesk

import (
	"context"
	"errors"
)

func elevate(context.Context, string, string) error {
	return errors.New("not supported on this system")
}
