package report

import (
	"slices"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// This file keeps the pre-fix (quadratic) implementations of mergeSeries and mergeHealth as a reference.
// The tests compare the linear implementation against them, so the merged result stays identical.

// refMergeSeries adds b, shifted by offset seconds, into a.
func refMergeSeries(a, b []SeriesPoint, offset int) []SeriesPoint {
	out := slices.Clone(a)
	for _, p := range b {
		sec := p.Second + offset
		i := slices.IndexFunc(out, func(q SeriesPoint) bool { return q.Second == sec })
		if i < 0 {
			p.Second = sec
			out = append(out, p)
			continue
		}
		q := &out[i]
		q.Requests += p.Requests
		q.Errors += p.Errors
		q.HTTP5xx += p.HTTP5xx
		q.NewConns += p.NewConns
		q.ReusedConns += p.ReusedConns
		q.Latency = mergeLatency(q.Latency, p.Latency)
		q.DNS = mergeLatency(q.DNS, p.DNS)
		q.Connect = mergeLatency(q.Connect, p.Connect)
		q.TLS = mergeLatency(q.TLS, p.TLS)
		q.Concurrency += p.Concurrency
	}
	slices.SortFunc(out, func(x, y SeriesPoint) int { return x.Second - y.Second })
	return out
}

// refMergeHealth adds b, shifted by offset seconds, into a.
func refMergeHealth(a, b []record.HealthPoint, offset int) []record.HealthPoint {
	out := slices.Clone(a)
	for _, p := range b {
		sec := p.Second + offset
		i := slices.IndexFunc(out, func(q record.HealthPoint) bool { return q.Second == sec })
		if i < 0 {
			p.Second = sec
			out = append(out, p)
			continue
		}
		out[i].Goroutines += p.Goroutines
		out[i].CPU = max(out[i].CPU, p.CPU)
	}
	slices.SortFunc(out, func(x, y record.HealthPoint) int { return x.Second - y.Second })
	return out
}

// refMergeSeriesHealth folds the series and health of reps one report at a time, the way MergeReports did before the fix.
func refMergeSeriesHealth(reps []*Report) ([]SeriesPoint, []record.HealthPoint) {
	var (
		first      time.Time
		allStarted = true
	)
	for _, r := range reps {
		if r.StartedAt.IsZero() {
			allStarted = false
		} else if first.IsZero() || r.StartedAt.Before(first) {
			first = r.StartedAt
		}
	}
	var (
		series []SeriesPoint
		health []record.HealthPoint
	)
	for _, r := range reps {
		offset := 0
		if allStarted {
			offset = int((r.StartedAt.Sub(first) + SeriesInterval/2) / SeriesInterval)
		}
		series = refMergeSeries(series, r.Series, offset)
		health = refMergeHealth(health, r.Health, offset)
	}
	return series, health
}
