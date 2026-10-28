//go:build !windows

package jotaro

import (
	"errors"
	"syscall"
)

// isConnRefused は err が接続拒否かを返す。
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
