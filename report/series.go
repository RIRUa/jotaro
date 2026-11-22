package report

import (
	"slices"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// SeriesInterval is the length of one interval of the time series.
const SeriesInterval = time.Second

// SeriesPoint is the aggregate for one second since the start of the run.
type SeriesPoint struct {
	// Second is the seconds elapsed since StartedAt (0-based). The interval is [Second, Second+1) seconds.
	Second int
	// Requests is the number of requests started in this interval.
	Requests int
	// Errors is how many of them ended in a network error (Err is not empty). HTTP 4xx/5xx are not included.
	Errors int
	// HTTP5xx is how many of them returned HTTP 5xx. It can overlap with Errors: a request whose status line
	// was 5xx but whose body then failed to arrive is counted in both (see #149).
	HTTP5xx int
	// NewConns / ReusedConns are, among the requests started in this interval, the number that were the first to use
	// a connection (the connection was opened for them) and the number that used a connection that had carried
	// an earlier request. See RequestStats.NewConns. Those that failed before getting a connection are counted in neither.
	NewConns    int
	ReusedConns int
	// Latency is the latency distribution of the requests started in this interval.
	Latency Latency
	// DNS / Connect / TLS are the distributions over the requests started in this interval in which that phase actually happened
	// (the same scope as the fields of the same name in RequestStats).
	DNS     Latency
	Connect Latency
	TLS     Latency
	// Concurrency is the maximum number of iterations running during this interval.
	Concurrency int
}

// buildSeries aggregates the records per second.
// It returns nil when there is no start time (origin is zero) or no records.
func buildSeries(origin time.Time, reqs []record.Record, iters []record.IterationRecord) []SeriesPoint {
	if origin.IsZero() {
		return nil
	}
	index := func(t time.Time) int { return max(int(t.Sub(origin)/SeriesInterval), 0) }

	// Up to the last interval that has records.
	n := 0
	for _, r := range reqs {
		n = max(n, index(r.Start)+1)
	}
	for _, it := range iters {
		n = max(n, index(it.Start.Add(it.Duration))+1)
	}
	if n == 0 {
		return nil
	}

	// Durations per second, to be turned into distributions later.
	var (
		lats     = make([][]time.Duration, n)
		dnss     = make([][]time.Duration, n)
		connects = make([][]time.Duration, n)
		tlss     = make([][]time.Duration, n)
	)
	pts := make([]SeriesPoint, n)
	for i := range pts {
		pts[i].Second = i
	}
	for _, r := range reqs {
		i := index(r.Start)
		pts[i].Requests++
		if r.Err != record.ErrNone {
			pts[i].Errors++
		}
		if r.Code >= 500 {
			pts[i].HTTP5xx++
		}
		switch r.Conn {
		case record.ConnNew:
			pts[i].NewConns++
		case record.ConnReused:
			pts[i].ReusedConns++
		}
		lats[i] = append(lats[i], r.Latency)
		if r.DNS > 0 {
			dnss[i] = append(dnss[i], r.DNS)
		}
		if r.Connect > 0 {
			connects[i] = append(connects[i], r.Connect)
		}
		if r.TLS > 0 {
			tlss[i] = append(tlss[i], r.TLS)
		}
	}
	for i := range pts {
		pts[i].Latency = newLatencyOwned(lats[i])
		pts[i].DNS, pts[i].Connect, pts[i].TLS = newLatencyOwned(dnss[i]), newLatencyOwned(connects[i]), newLatencyOwned(tlss[i])
	}

	// Concurrency is found by sweeping the starts (+1) and ends (-1) in time order and taking the maximum per interval.
	type event struct {
		at    time.Duration
		delta int
	}
	events := make([]event, 0, 2*len(iters))
	ends := iterationEnds(origin, iters)
	for i, it := range iters {
		events = append(events, event{max(it.Start.Sub(origin), 0), +1}, event{ends[i], -1})
	}
	// At the same instant, ends are handled first (so back-to-back iterations are not counted as overlapping).
	slices.SortFunc(events, func(a, b event) int {
		if a.at != b.at {
			return int(a.at - b.at)
		}
		return a.delta - b.delta
	})
	cur, e := 0, 0
	for i := range pts {
		end := time.Duration(i+1) * SeriesInterval
		peak := cur // the value at the start of the interval
		for ; e < len(events) && events[e].at < end; e++ {
			cur += events[e].delta
			peak = max(peak, cur)
		}
		pts[i].Concurrency = peak
	}
	return pts
}

// mergeSeries adds the series in parts into one, each shifted by the matching offset in seconds.
// Counts are summed, and concurrency is summed on the assumption that the runs overlapped (an upper bound).
// Points of the same second are combined in the order of parts. The result is ordered by Second, and nil when there are no points.
func mergeSeries(parts [][]SeriesPoint, offsets []int) []SeriesPoint {
	return mergeBySecond(parts, offsets,
		func(p SeriesPoint) int { return p.Second }, func(p *SeriesPoint, s int) { p.Second = s },
		func(q *SeriesPoint, p SeriesPoint) {
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
		})
}

// iterationEnds returns the end of each iteration as an offset from origin, in the order of iters.
//
// A user runs its iterations one after another, so an iteration never overlaps the next one of the same user.
// Records read back from JSON (FromRaw) have a wall clock reading only, while Duration was measured on the monotonic clock;
// the two clocks disagree by a little (up to about a millisecond on Windows), which would make back-to-back iterations of
// one user overlap and count one more running iteration. The end of an iteration is therefore cut at the start of the
// user's next iteration (Iteration + 1 of the same scenario and user) when it would pass it.
// Iterations of different users, and reports with no such overlap, are unchanged.
func iterationEnds(origin time.Time, iters []record.IterationRecord) []time.Duration {
	type key struct {
		scenario  string
		user, num int
	}
	start := make([]time.Duration, len(iters))
	ends := make([]time.Duration, len(iters))
	byKey := make(map[key]int, len(iters)) // the position of each iteration, to find the next one
	for i, it := range iters {
		start[i] = max(it.Start.Sub(origin), 0)
		ends[i] = start[i] + max(it.Duration, 0)
		byKey[key{it.Scenario, it.UserID, it.Iteration}] = i
	}
	for i, it := range iters {
		if next, ok := byKey[key{it.Scenario, it.UserID, it.Iteration + 1}]; ok && start[next] >= start[i] {
			ends[i] = min(ends[i], start[next])
		}
	}
	return ends
}
