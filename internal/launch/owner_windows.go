package launch

import "os"

// Windows extra dirs are per-user (%LOCALAPPDATA%, Program Files).
func ownedBySelfOrRoot(os.FileInfo) bool { return true }
