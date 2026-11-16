package report

import (
	"fmt"
	"io"
	"math"
	"time"
)

// Delta is one compared metric of two runs.
type Delta struct {
	// Scope is "overall" or "scenario/step".
	Scope string
	// Metric is the metric name, such as "p95" or "rps".
	Metric string
	// Base and New are the values in the base and the new run.
	// Durations are in nanoseconds and ratios are 0 to 1.
	Base, New float64
	// HigherIsBetter is true for metrics such as rps, where an increase is an improvement.
	HigherIsBetter bool
}

// Change is the relative change from Base to New, in percent.
// It is NaN when Base is 0 and New is not, because the change cannot be expressed as a ratio.
func (d Delta) Change() float64 {
	switch {
	case d.Base == 0 && d.New == 0:
		return 0
	case d.Base == 0:
		return math.NaN()
	}
	return (d.New - d.Base) / d.Base * 100
}

// Regression is the relative worsening in percent: positive when the new run is worse,
// negative when it is better. It is +Inf when Base is 0 and the new run is worse.
func (d Delta) Regression() float64 {
	diff := d.New - d.Base
	if d.HigherIsBetter {
		diff = -diff
	}
	switch {
	case diff == 0:
		return 0
	case d.Base == 0:
		return math.Inf(sign(diff))
	}
	return diff / math.Abs(d.Base) * 100
}

func sign(f float64) int {
	if f < 0 {
		return -1
	}
	return 1
}

// Comparison is the result of Compare.
type Comparison struct {
	Deltas []Delta
}

// Compare diffs two results. The overall metrics come first, then one set per step
// that exists in both runs (matched by scenario and step name). Metrics without data
// in either run are left out.
func Compare(base, cur *Report) Comparison {
	var c Comparison
	add := func(scope, metric string, higher bool, b, n float64) {
		c.Deltas = append(c.Deltas, Delta{Scope: scope, Metric: metric, Base: b, New: n, HigherIsBetter: higher})
	}
	add("overall", "rps", true, base.RPS(), cur.RPS())
	c.addRequests("overall", base.Requests, cur.Requests, add)

	for _, bs := range base.Scenarios {
		for _, cs := range cur.Scenarios {
			if bs.Name != cs.Name {
				continue
			}
			for _, bst := range bs.Steps {
				for _, cst := range cs.Steps {
					if bst.Name == cst.Name {
						c.addRequests(bs.Name+"/"+bst.Name, bst.Requests, cst.Requests, add)
					}
				}
			}
		}
	}
	return c
}

// addRequests adds the latency and error metrics of one scope when both sides have requests.
func (c *Comparison) addRequests(scope string, b, n RequestStats, add func(scope, metric string, higher bool, b, n float64)) {
	if b.Latency.Count == 0 || n.Latency.Count == 0 {
		return
	}
	for _, s := range []struct {
		name string
		get  func(Latency) time.Duration
	}{
		{"p50", func(l Latency) time.Duration { return l.P50 }},
		{"p95", func(l Latency) time.Duration { return l.P95 }},
		{"p99", func(l Latency) time.Duration { return l.P99 }},
	} {
		add(scope, s.name, false, float64(s.get(b.Latency)), float64(s.get(n.Latency)))
	}
	add(scope, "error_rate", false, errorRate(b), errorRate(n))
}

// errorRate is the share of requests that ended in a network error.
func errorRate(q RequestStats) float64 {
	if q.Count == 0 {
		return 0
	}
	e := 0
	for _, c := range q.Errors {
		e += c
	}
	return float64(e) / float64(q.Count)
}

// Violations returns the p95 deltas that got worse by more than maxPct percent.
func (c Comparison) Violations(maxPct float64) []Delta {
	var out []Delta
	for _, d := range c.Deltas {
		if d.Metric == "p95" && d.Regression() > maxPct {
			out = append(out, d)
		}
	}
	return out
}

// Print writes the comparison as a table.
func (c Comparison) Print(w io.Writer) {
	fmt.Fprintf(w, "%-24s %-11s %12s %12s %9s\n", "SCOPE", "METRIC", "BASE", "NEW", "CHANGE")
	for _, d := range c.Deltas {
		t := Threshold{Metric: d.Metric}
		change := "n/a"
		if v := d.Change(); !math.IsNaN(v) {
			change = fmt.Sprintf("%+.1f%%", v)
		}
		mark := ""
		if r := d.Regression(); r > 0 {
			mark = " worse"
		} else if r < 0 {
			mark = " better"
		}
		fmt.Fprintf(w, "%-24s %-11s %12s %12s %9s%s\n", d.Scope, d.Metric, t.formatValue(d.Base), t.formatValue(d.New), change, mark)
	}
}
