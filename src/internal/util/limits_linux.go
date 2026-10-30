//go:build linux

package util

import (
	"golang.org/x/sys/unix"
)

// RaiseFDLimit raises the open-file (socket) soft limit (RLIMIT_NOFILE.Cur)
// toward the hard limit (.Max). want <= 0 means "raise to the hard limit".
// It returns the limit in effect and whether it was changed. On other systems
// the process keeps whatever ulimit gave it, so the portable build returns
// (0, false).
func RaiseFDLimit(want int) (int, bool) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return int(lim.Cur), false
	}
	target := lim.Max
	if want > 0 && uint64(want) < target {
		target = uint64(want)
	}
	if target == 0 || lim.Cur >= target {
		return int(lim.Cur), false
	}
	lim.Cur = target
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return int(lim.Cur), false
	}
	return int(target), true
}
