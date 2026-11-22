//go:build !race

package jotaro

// raceEnabled reports whether the test binary is built with the race detector, which changes allocation counts.
const raceEnabled = false
