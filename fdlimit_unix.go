//go:build unix

package jotaro

import "syscall"

// fdLimit is the soft limit on open file descriptors (ulimit -n).
// Every connection holds one descriptor, so it bounds the usable concurrency.
func fdLimit() (uint64, bool) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0, false
	}
	return uint64(rl.Cur), true //nolint:unconvert // Rlimit.Cur is int64 on some platforms
}
