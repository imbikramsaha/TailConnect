//go:build !darwin

package rustdesk

// NeedsRepair reports settings the user's RustDesk can't read; only
// TailConnect 0.3.9 on macOS left such files.
func NeedsRepair() bool { return false }
