//go:build !windows

package autostart

import "errors"

func setWindows(bool, string) error { return errors.New("not windows") }
