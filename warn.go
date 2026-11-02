package jotaro

import (
	"fmt"
	"os"
)

// fdReserve is the number of descriptors assumed to be used by things other than
// connections (stdio, the raw-data file, the runtime's own descriptors, and so on).
const fdReserve = 64

// warn reports a non-fatal problem found before the run starts, through Plan.Warn.
// Without Plan.Warn it writes to standard error, because a silent warning helps nobody.
func (p Plan) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.Warn != nil {
		p.Warn(msg)
		return
	}
	fmt.Fprintln(os.Stderr, "jotaro: warning: "+msg)
}

// checkFDLimit warns when the concurrency may need more descriptors than the OS allows.
// Beyond the limit, new connections fail with "too many open files", which would show up
// in the report as errors caused by the load generator, not by the target.
func (p Plan) checkFDLimit() {
	limit, ok := fdLimit()
	if !ok {
		return
	}
	if need := uint64(p.maxConcurrency()) + fdReserve; need > limit {
		p.warn("up to %d concurrent connections may be needed but the open file limit is %d (ulimit -n); "+
			"requests may fail with \"too many open files\"", p.maxConcurrency(), limit)
	}
}
