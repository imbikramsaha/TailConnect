//go:build !windows

package launch

import (
	"os"
	"syscall"
)

func ownedBySelfOrRoot(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == 0 || int(st.Uid) == os.Getuid()
}
