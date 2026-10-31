//go:build !unix

package jotaro

import "time"

// processCPUTime cannot get the CPU time on this platform.
func processCPUTime() (time.Duration, bool) { return 0, false }
