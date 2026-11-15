//go:build !darwin && !windows && !linux

package window

func command(string) ([]string, bool) { return nil, false }
