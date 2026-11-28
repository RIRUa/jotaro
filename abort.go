package jotaro

import (
	"errors"
	"fmt"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// abortInterval is how often Plan.AbortOn is evaluated.
const abortInterval = time.Second

// ErrAborted is wrapped by the error Run returns when Plan.AbortOn stopped the run early.
// The report returned together with it covers the run up to the abort. Test for it with errors.Is.
var ErrAborted = errors.New("jotaro: run aborted")

// AbortRule is one condition of Plan.AbortOn: stop the run as soon as When holds.
//
// When uses the threshold syntax of report.ParseThreshold, but it describes the state to stop at rather than
// the state to accept. So "http_5xx_rate>50%" aborts once more than half of the requests got a 5xx response.
// The value is checked the same way: NaN, Inf, a negative value and a ratio above 100% (or above 1 in the plain form)
// are errors, because such a rule could never fire or could never be meant. For example:
//
//	AbortOn: []jotaro.AbortRule{
//		{When: "http_5xx_rate>50%", For: 5 * time.Second},
//		{When: "error_rate>50%", For: 5 * time.Second},
//		{When: "p95>2s", For: 10 * time.Second},
//	},
//
// Mind which metric sees the failure: error_rate is the share of requests that ended in a transport-level error
// (timeout, connection refused, DNS failure and so on). A failed Expect* check is not a request error: it fails the
// step with expect_mismatch and reaches iteration_fail_rate when the scenario returns that error. An HTTP error
// status is counted by http_4xx_rate / http_5xx_rate.
//
// The condition is evaluated every second against the whole run so far (not a sliding window), the same way
// Report.Check reads a finished report. Percentiles are therefore the approximate ones of a run without Plan.Raw.
// Nothing counts as holding while there is no data yet (for example before the first request has finished,
// or before a step named in a {step=...} filter has run).
type AbortRule struct {
	// When is the condition, such as "error_rate>50%".
	When string
	// For is how long When must hold without a break before the run is aborted. 0 aborts at the first
	// evaluation where it holds. A value of a few seconds keeps one early failure from stopping a run
	// whose statistics have not settled yet.
	For time.Duration
}

// abortRule is an AbortRule with its condition parsed.
type abortRule struct {
	rule AbortRule
	t    report.Threshold
}

// parseAbortRules parses the conditions of Plan.AbortOn. It is called by validate, so a typo fails before any load is sent.
func parseAbortRules(rules []AbortRule) ([]abortRule, error) {
	out := make([]abortRule, 0, len(rules))
	for i, r := range rules {
		if r.For < 0 {
			return nil, fmt.Errorf("jotaro: Plan.AbortOn[%d].For must be at least 0", i)
		}
		t, err := report.ParseThreshold(r.When)
		if err != nil {
			return nil, fmt.Errorf("jotaro: Plan.AbortOn[%d]: %w", i, err)
		}
		out = append(out, abortRule{rule: r, t: t})
	}
	return out, nil
}

// abortState remembers, for each rule, since when its condition has been holding without a break.
type abortState struct {
	rules []abortRule
	since []time.Time // zero while the condition does not hold
}

func newAbortState(rules []abortRule) *abortState {
	return &abortState{rules: rules, since: make([]time.Time, len(rules))}
}

// step feeds the evaluation at time now (held[i] is whether rules[i] holds) and returns the first rule
// that has held for its whole For, if any.
func (s *abortState) step(now time.Time, held []bool) (abortRule, bool) {
	var hit abortRule
	found := false
	for i, r := range s.rules {
		if !held[i] {
			s.since[i] = time.Time{}
			continue
		}
		if s.since[i].IsZero() {
			s.since[i] = now
		}
		if !found && now.Sub(s.since[i]) >= r.rule.For {
			hit, found = r, true
		}
	}
	return hit, found
}

// holdingRules reports for each rule whether its condition holds in rep. A result that could not be
// judged (no data, no matching step) does not hold. With several matching steps, one is enough.
func holdingRules(rules []abortRule, rep *report.Report) []bool {
	held := make([]bool, len(rules))
	for i, r := range rules {
		for _, c := range rep.Check(r.t) {
			if c.Pass {
				held[i] = true
				break
			}
		}
	}
	return held
}

// watchAbort evaluates rules every abortInterval against what agg has aggregated so far. When a rule fires it
// calls onAbort once with a description and stops watching. The returned function stops the watcher and
// waits for it, so onAbort is never running after it returns.
func watchAbort(rules []abortRule, agg *report.Aggregator, col *record.Collector, start time.Time, onAbort func(reason string)) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(abortInterval)
		defer t.Stop()
		st := newAbortState(rules)
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				rep := agg.Snapshot(now.Sub(start)) // rules never read the time series
				rep.Dropped = col.Dropped()
				r, ok := st.step(now, holdingRules(rules, rep))
				if !ok {
					continue
				}
				reason := r.rule.When
				if r.rule.For > 0 {
					reason += fmt.Sprintf(" for %s", r.rule.For)
				}
				onAbort(fmt.Sprintf("%s (at %s)", reason, now.Sub(start).Round(time.Millisecond)))
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
