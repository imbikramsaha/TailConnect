package rustdesk

import "os"

// Windows: Lstat already refused reparse points (symlinks/junctions).
func openNoFollow(p string) (*os.File, error) { return os.Open(p) }
