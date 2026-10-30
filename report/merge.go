package report

import (
	"errors"
	"fmt"
	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
	"math"
	"time"
)

// MergeReports combines several reports into one.
// Use it to see the results of a run split across several load generators as a single run.
//
//   - Counts, bytes, statuses and errors are summed.
//   - The run time goes from the first StartedAt to the last end (StartedAt + Elapsed).
//     If a report without StartedAt (such as version 1 JSON) is mixed in,
//     the maximum Elapsed is used (assuming they ran at the same time). RPS is total count / Elapsed.
//   - Series are shifted by the difference of StartedAt, overlaid, and added per interval.
//     Concurrency is summed assuming everything ran at the same time (an upper-bound estimate).
//   - Scenarios and steps are matched by name, in the order they first appear.
//   - Min / Max / Mean are exact. P50 to P99 are recomputed by adding the histograms,
//     so they are approximations within 1% relative error. If reports without a histogram
//     (such as version 1 JSON) are mixed in, a count-weighted average is used (with a larger error).
//
// Reports whose Open differs cannot be mixed, so an error is returned.
func MergeReports(reps ...*Report) (*Report, error) {
	if len(reps) == 0 {
		return nil, errors.New("jotaro: merge: no reports")
	}
	out := &Report{Open: reps[0].Open}
	var reportSeries []*Report
	var (
		first, last time.Time
		allStarted  = true // whether every report has StartedAt
	)
	var (
		scenarios []*ScenarioReport
		byName    = map[string]*ScenarioReport{}
		stepAt    = map[string]map[string]int{} // scenario name → step name → index into Steps
	)
	for i, r := range reps {
		if r.Open != out.Open {
			return nil, fmt.Errorf("jotaro: merge: report #%d has Open=%t, but #1 has Open=%t", i+1, r.Open, out.Open)
		}
		out.Dropped += r.Dropped
		out.Elapsed = max(out.Elapsed, r.Elapsed)
		if r.StartedAt.IsZero() {
			allStarted = false
		} else {
			if first.IsZero() || r.StartedAt.Before(first) {
				first = r.StartedAt
			}
			if end := r.StartedAt.Add(r.Elapsed); end.After(last) {
				last = end
			}
		}
		out.Requests = mergeRequests(out.Requests, r.Requests)
		reportSeries = append(reportSeries, r)

		for _, sc := range r.Scenarios {
			dst, ok := byName[sc.Name]
			if !ok {
				dst = &ScenarioReport{Name: sc.Name}
				byName[sc.Name] = dst
				stepAt[sc.Name] = map[string]int{}
				scenarios = append(scenarios, dst)
			}
			dst.Iterations = mergeIterations(dst.Iterations, sc.Iterations)
			for _, st := range sc.Steps {
				j, ok := stepAt[sc.Name][st.Name]
				if !ok {
					dst.Steps = append(dst.Steps, StepStats{Name: st.Name})
					j = len(dst.Steps) - 1
					stepAt[sc.Name][st.Name] = j
				}
				dst.Steps[j] = mergeStep(dst.Steps[j], st)
			}
		}
	}
	if allStarted {
		out.StartedAt = first
		out.Elapsed = last.Sub(first)
	}
	// Time series are overlaid, shifted by the difference in StartedAt. If any report lacks StartedAt, their starts are aligned.
	for _, r := range reportSeries {
		offset := 0
		if allStarted {
			offset = int((r.StartedAt.Sub(first) + SeriesInterval/2) / SeriesInterval)
		}
		out.Series = mergeSeries(out.Series, r.Series, offset)
		out.Health = mergeHealth(out.Health, r.Health, offset)
	}
	for _, sc := range scenarios {
		out.Scenarios = append(out.Scenarios, *sc)
	}
	return out, nil
}

// mergeStep adds two StepStats of the same step by summing the counts and merging the distributions.
func mergeStep(a, b StepStats) StepStats {
	return StepStats{
		Name:     a.Name,
		Count:    a.Count + b.Count,
		Failed:   a.Failed + b.Failed,
		Duration: mergeLatency(a.Duration, b.Duration),
		Errors:   mergeErrs(a.Errors, b.Errors),
		Requests: mergeRequests(a.Requests, b.Requests),
	}
}

// mergeIterations adds two IterationStats in the same way as mergeStep.
func mergeIterations(a, b IterationStats) IterationStats {
	return IterationStats{
		Count:  a.Count + b.Count,
		Failed: a.Failed + b.Failed,
		Active: mergeLatency(a.Active, b.Active),
		Total:  mergeLatency(a.Total, b.Total),
		Errors: mergeErrs(a.Errors, b.Errors),
	}
}

// mergeRequests adds two RequestStats: counts and sizes are summed and distributions merged.
func mergeRequests(a, b RequestStats) RequestStats {
	out := RequestStats{
		Count:       a.Count + b.Count,
		Latency:     mergeLatency(a.Latency, b.Latency),
		TTFB:        mergeLatency(a.TTFB, b.TTFB),
		Corrected:   mergeLatency(a.Corrected, b.Corrected),
		DNS:         mergeLatency(a.DNS, b.DNS),
		Connect:     mergeLatency(a.Connect, b.Connect),
		TLS:         mergeLatency(a.TLS, b.TLS),
		NewConns:    a.NewConns + b.NewConns,
		ReusedConns: a.ReusedConns + b.ReusedConns,
		Errors:      mergeErrs(a.Errors, b.Errors),
		BytesIn:     a.BytesIn + b.BytesIn,
		BytesOut:    a.BytesOut + b.BytesOut,
	}
	for _, m := range []map[string]int{a.Status, b.Status} {
		for k, v := range m {
			if out.Status == nil {
				out.Status = map[string]int{}
			}
			out.Status[k] += v
		}
	}
	return out
}

// mergeErrs sums two error tallies. The result stays nil when both are empty.
func mergeErrs(a, b map[record.ErrKind]int) map[record.ErrKind]int {
	var out map[record.ErrKind]int
	for _, m := range []map[record.ErrKind]int{a, b} {
		for k, v := range m {
			if out == nil {
				out = map[record.ErrKind]int{}
			}
			out[k] += v
		}
	}
	return out
}

// mergeLatency combines two distributions. A side with Count 0 is ignored.
func mergeLatency(a, b Latency) Latency {
	switch {
	case a.Count == 0:
		return b
	case b.Count == 0:
		return a
	}
	wa, wb := float64(a.Count), float64(b.Count)
	avg := func(x, y time.Duration) time.Duration {
		return time.Duration(math.Round((float64(x)*wa + float64(y)*wb) / (wa + wb)))
	}
	out := Latency{
		Count: a.Count + b.Count,
		Min:   min(a.Min, b.Min),
		Mean:  avg(a.Mean, b.Mean),
		Max:   max(a.Max, b.Max),
	}
	// Percentiles are computed from the distribution only when both sides have a histogram that matches their counts.
	if a.hist.Total() == a.Count && b.hist.Total() == b.Count {
		out.hist = histogram.Merge(a.hist, b.hist)
		// A representative value can fall outside the range, so clamp it to the observed Min / Max.
		pct := func(p int) time.Duration { return min(max(out.hist.Percentile(p), out.Min), out.Max) }
		out.P50, out.P90, out.P95, out.P99 = pct(50), pct(90), pct(95), pct(99)
		return out
	}
	out.P50, out.P90, out.P95, out.P99 = avg(a.P50, b.P50), avg(a.P90, b.P90), avg(a.P95, b.P95), avg(a.P99, b.P99)
	return out
}
