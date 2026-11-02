//go:build !unix

package jotaro

// fdLimit cannot get the descriptor limit on this platform.
func fdLimit() (uint64, bool) { return 0, false }
