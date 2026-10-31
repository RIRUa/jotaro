//go:build !windows

package jotaro

import (
	"errors"
	"syscall"
)

// isConnRefused reports whether err is a refused connection.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
