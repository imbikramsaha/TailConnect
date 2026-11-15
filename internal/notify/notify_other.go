//go:build !darwin && !linux && !windows

package notify

func send(string, string, string) error { return nil }
