//go:build windows

package jotaro

import (
	"errors"
	"syscall"
)

// wsaeConnRefused is WSAECONNREFUSED. Windows' syscall.ECONNREFUSED differs from
// the error value actually returned, so errors.Is does not match it.
const wsaeConnRefused = syscall.Errno(10061)

// isConnRefused reports whether err is a refused connection.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaeConnRefused)
}
