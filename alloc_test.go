package jotaro

import (
	"context"
	"testing"
	"time"
)

// TestIterationAllocations guards the allocation count of one iteration against a stub transport, that is, the
// part that belongs to the generator and to net/http's client but not to a network. An allocation count does
// not depend on the speed of the machine, so it can run in CI where a timing could not. The bounds are generous:
// they sit roughly 15-20% above what Go 1.27 on arm64 measured after #141 (66, 76 and 326; Go 1.25.0 measured 69, 71 and 345), and below what the same code measured before
// it (91, 99 and 407 allocations), so they fail on a return to the old cost and not on a change of Go version.
// If a bound fails after a legitimate change, measure with BenchmarkIteration* and raise it, with the reason.
func TestIterationAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	cases := []struct {
		name  string
		sc    *Scenario
		bound float64
	}{
		{"single GET", singleGetScenario(), 80},
		{"POST JSON", postJSONScenario(), 92},
		{"five steps", fiveStepsScenario(), 380},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := newBenchRunner(stubTransport{body: `{"ok":true}`}, c.sc)
			ctx := context.Background()
			at := time.Now()
			i := 0
			got := testing.AllocsPerRun(200, func() {
				u := newUser(ctx, br.p, c.sc, i, 0, br.tr, 30*time.Second, br.col)
				u.scheduled.set(at)
				if err := c.sc.run(u); err != nil {
					t.Fatal(err)
				}
				i++
			})
			t.Logf("%s: %.0f allocations per iteration (bound %.0f)", c.name, got, c.bound)
			if got > c.bound {
				t.Errorf("%s: %.0f allocations per iteration, want at most %.0f", c.name, got, c.bound)
			}
		})
	}
}
