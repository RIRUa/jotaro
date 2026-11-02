package jotaro

import (
	"strings"
	"testing"
)

func TestCheckFDLimitWarns(t *testing.T) {
	limit, ok := fdLimit()
	if !ok {
		t.Skip("file descriptor limit is not available on this platform")
	}
	var got []string
	p := Plan{Users: int(limit) + 1, Warn: func(msg string) { got = append(got, msg) }}
	p.checkFDLimit()
	if len(got) != 1 || !strings.Contains(got[0], "ulimit -n") {
		t.Fatalf("warnings = %q, want one mentioning ulimit -n", got)
	}
}

func TestCheckFDLimitQuietWhenWithinLimit(t *testing.T) {
	var got []string
	p := Plan{Users: 1, Warn: func(msg string) { got = append(got, msg) }}
	p.checkFDLimit()
	if len(got) != 0 {
		t.Fatalf("warnings = %q, want none", got)
	}
}
