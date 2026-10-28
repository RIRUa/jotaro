//go:build windows

package jotaro

import (
	"errors"
	"syscall"
)

// wsaeConnRefused は WSAECONNREFUSED。Windows の syscall.ECONNREFUSED は
// 実際に返ってくるエラー値と別物なので、errors.Is では一致しない。
const wsaeConnRefused = syscall.Errno(10061)

// isConnRefused は err が接続拒否かを返す。
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaeConnRefused)
}
