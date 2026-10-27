//go:build !unix

package jotaro

import "time"

// processCPUTime は、この環境では CPU 時間を取得できない。
func processCPUTime() (time.Duration, bool) { return 0, false }
