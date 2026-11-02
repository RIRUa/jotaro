package report

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
)

// Aggregator builds a Report incrementally while a run is in progress.
//
// Unlike BuildReport, it never holds the records. Each one is folded into counters and
// log histograms, so memory depends on the number of distinct steps and on the length of the run in seconds,
// not on the number of requests.
//
// The price is precision: Count, Min, Max and Mean are exact, but P50 to P99 are approximations
// within 1% relative error (the same as after MergeReports). Use BuildReport when exact percentiles are needed.
//
// It implements record.Observer, so it can be given to record.NewStreamingCollector.
// All methods are safe for concurrent use.
type Aggregator struct {
	// origin is time zero of the time series. Second i covers [origin+i, origin+i+1) seconds.
	origin time.Time

	mu        sync.Mutex
	seq       int // creation counter; breaks ties when ordering scenarios and steps
	all       reqAcc
	scenarios map[string]*scenarioAcc
	secs      []secAcc
	conc      concurrency
}

// NewAggregator returns an Aggregator whose time series starts at origin (normally the start of the run).
func NewAggregator(origin time.Time) *Aggregator {
	return &Aggregator{origin: origin, scenarios: map[string]*scenarioAcc{}}
}

// latAcc accumulates a distribution: exact count, sum, min and max, and a histogram for the percentiles.
type latAcc struct {
	n        int
	sum      time.Duration
	min, max time.Duration
	h        histogram.H
}

// add counts one observation.
func (a *latAcc) add(d time.Duration) {
	if a.n == 0 || d < a.min {
		a.min = d
	}
	if a.n == 0 || d > a.max {
		a.max = d
	}
	a.n++
	a.sum += d
	a.h.Add(d)
}

// latency summarizes the observations. The percentiles are clamped to [Min, Max],
// which the representative value of a bucket may otherwise slightly exceed.
func (a *latAcc) latency() Latency {
	if a.n == 0 {
		return Latency{}
	}
	pct := func(p int) time.Duration { return min(max(a.h.Percentile(p), a.min), a.max) }
	return Latency{
		Count: a.n,
		Min:   a.min,
		Mean:  a.sum / time.Duration(a.n),
		P50:   pct(50),
		P90:   pct(90),
		P95:   pct(95),
		P99:   pct(99),
		Max:   a.max,
		hist:  a.h,
	}
}

// reqAcc accumulates RequestStats.
type reqAcc struct {
	count                 int
	lat, corrected, ttfb  latAcc
	dns, connect, tls     latAcc
	newConns, reusedConns int
	status                map[string]int
	errors                map[record.ErrKind]int
	bytesIn, bytesOut     int64
}

// add folds one request in. It follows the same rules as requestStats.
func (q *reqAcc) add(r *record.Record) {
	q.count++
	q.lat.add(r.Latency)
	q.corrected.add(r.Corrected())
	if r.TTFB > 0 {
		q.ttfb.add(r.TTFB)
	}
	if r.DNS > 0 {
		q.dns.add(r.DNS)
	}
	if r.Connect > 0 {
		q.connect.add(r.Connect)
	}
	if r.TLS > 0 {
		q.tls.add(r.TLS)
	}
	switch r.Conn {
	case record.ConnNew:
		q.newConns++
	case record.ConnReused:
		q.reusedConns++
	}
	if r.Code > 0 {
		if q.status == nil {
			q.status = map[string]int{}
		}
		q.status[statusClass(r.Code)]++
	}
	if r.Err != record.ErrNone {
		q.errors = count(q.errors, r.Err)
	}
	q.bytesIn += r.BytesIn
	q.bytesOut += r.BytesOut
}

// stats turns the accumulated values into RequestStats.
func (q *reqAcc) stats() RequestStats {
	return RequestStats{
		Count:       q.count,
		Latency:     q.lat.latency(),
		TTFB:        q.ttfb.latency(),
		DNS:         q.dns.latency(),
		Connect:     q.connect.latency(),
		TLS:         q.tls.latency(),
		NewConns:    q.newConns,
		ReusedConns: q.reusedConns,
		Corrected:   q.corrected.latency(),
		Status:      q.status,
		Errors:      q.errors,
		BytesIn:     q.bytesIn,
		BytesOut:    q.bytesOut,
	}
}

// statusClasses holds the labels of the usual status classes, so the hot path does not allocate.
var statusClasses = [...]string{"0xx", "1xx", "2xx", "3xx", "4xx", "5xx", "6xx", "7xx", "8xx", "9xx"}

// statusClass is the label of an HTTP status class such as "2xx".
func statusClass(code int) string {
	if c := code / 100; c >= 0 && c < len(statusClasses) {
		return statusClasses[c]
	}
	return fmt.Sprintf("%dxx", code/100)
}

// scenarioAcc accumulates one scenario.
type scenarioAcc struct {
	name string
	seq  int
	// iterFirst / stepFirst are the earliest start of an iteration / a step. Zero if there is none.
	iterFirst, stepFirst time.Time

	iters, failed int
	errors        map[record.ErrKind]int
	active, total latAcc

	steps map[string]*stepAcc
}

// stepAcc accumulates one step of a scenario.
type stepAcc struct {
	name string
	seq  int
	// first is the earliest start of the step. Zero while only requests have been seen.
	first time.Time
	// seen is whether a step record has arrived. Requests of a step without any step record are left out of the step list.
	seen bool

	count, failed int
	errors        map[record.ErrKind]int
	dur           latAcc
	reqs          reqAcc
}

// secAcc accumulates one second of the time series.
type secAcc struct {
	requests, errors, http5xx int
	newConns, reusedConns     int
	lat, dns, connect, tls    latAcc
}

// concurrency tracks the number of running iterations and its maximum in each second.
type concurrency struct {
	cur   int
	peaks []int // peaks[i] is the maximum during second i, for the seconds reached so far
}

// change applies delta at second sec. An event for an earlier second than one already seen
// (goroutines can report slightly out of order) is counted in the latest second.
func (c *concurrency) change(sec, delta int) {
	sec = max(sec, len(c.peaks)-1, 0)
	for len(c.peaks) <= sec {
		// A new second starts at the current value, as the old sweep did.
		c.peaks = append(c.peaks, c.cur)
	}
	c.cur += delta
	c.peaks[sec] = max(c.peaks[sec], c.cur)
}

// peak returns the maximum during second sec, which is the current value for seconds with no event.
func (c *concurrency) peak(sec int) int {
	if sec < len(c.peaks) {
		return c.peaks[sec]
	}
	return c.cur
}

// index returns the number of the time series second that t falls in.
func (a *Aggregator) index(t time.Time) int {
	return max(int(t.Sub(a.origin)/SeriesInterval), 0)
}

// sec returns the series entry for second i, extending the series as needed.
func (a *Aggregator) sec(i int) *secAcc {
	for len(a.secs) <= i {
		a.secs = append(a.secs, secAcc{})
	}
	return &a.secs[i]
}

// scenario returns the accumulator for name, creating it on first use. The caller holds a.mu.
func (a *Aggregator) scenario(name string) *scenarioAcc {
	sc, ok := a.scenarios[name]
	if !ok {
		a.seq++
		sc = &scenarioAcc{name: name, seq: a.seq, steps: map[string]*stepAcc{}}
		a.scenarios[name] = sc
	}
	return sc
}

// step returns the accumulator for a step of sc, creating it on first use. The caller holds a.mu.
func (a *Aggregator) step(sc *scenarioAcc, name string) *stepAcc {
	st, ok := sc.steps[name]
	if !ok {
		a.seq++
		st = &stepAcc{name: name, seq: a.seq}
		sc.steps[name] = st
	}
	return st
}

// earliest returns the earlier of cur and t, treating a zero cur as unset.
func earliest(cur, t time.Time) time.Time {
	if cur.IsZero() || t.Before(cur) {
		return t
	}
	return cur
}

// ObserveRequest folds in one finished request.
func (a *Aggregator) ObserveRequest(r record.Record) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.all.add(&r)
	a.step(a.scenario(r.Scenario), r.Step).reqs.add(&r)

	s := a.sec(a.index(r.Start))
	s.requests++
	if r.Err != record.ErrNone {
		s.errors++
	}
	if r.Code >= 500 {
		s.http5xx++
	}
	switch r.Conn {
	case record.ConnNew:
		s.newConns++
	case record.ConnReused:
		s.reusedConns++
	}
	s.lat.add(r.Latency)
	if r.DNS > 0 {
		s.dns.add(r.DNS)
	}
	if r.Connect > 0 {
		s.connect.add(r.Connect)
	}
	if r.TLS > 0 {
		s.tls.add(r.TLS)
	}
}

// ObserveStep folds in one finished step.
func (a *Aggregator) ObserveStep(r record.StepRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()

	sc := a.scenario(r.Scenario)
	sc.stepFirst = earliest(sc.stepFirst, r.Start)
	st := a.step(sc, r.Step)
	st.seen = true
	st.first = earliest(st.first, r.Start)
	st.count++
	if r.Err != record.ErrNone {
		st.failed++
		st.errors = count(st.errors, r.Err)
	}
	st.dur.add(r.Duration)
}

// ObserveIteration folds in one finished scenario run.
func (a *Aggregator) ObserveIteration(r record.IterationRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()

	sc := a.scenario(r.Scenario)
	sc.iterFirst = earliest(sc.iterFirst, r.Start)
	sc.iters++
	if r.Err != record.ErrNone {
		sc.failed++
		sc.errors = count(sc.errors, r.Err)
	}
	sc.active.add(r.Active())
	sc.total.add(r.Duration)

	end := a.index(r.Start.Add(r.Duration))
	a.sec(end) // the series reaches at least the second in which an iteration ended
	a.conc.change(end, -1)
}

// ObserveIterationStart counts one more running iteration, for the concurrency of the time series.
func (a *Aggregator) ObserveIterationStart(t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conc.change(a.index(t), +1)
}

// Report returns the result so far. elapsed is the length of the run.
// Open, Dropped and Health are not known here; the caller sets them.
func (a *Aggregator) Report(elapsed time.Duration) *Report {
	a.mu.Lock()
	defer a.mu.Unlock()

	rep := &Report{StartedAt: wallUTC(a.origin), Elapsed: elapsed, Requests: a.all.stats()}
	rep.Series = a.series()

	scs := make([]*scenarioAcc, 0, len(a.scenarios))
	for _, sc := range a.scenarios {
		scs = append(scs, sc)
	}
	slices.SortFunc(scs, compareScenarios)

	for _, sc := range scs {
		sr := ScenarioReport{
			Name: sc.name,
			Iterations: IterationStats{
				Count:  sc.iters,
				Failed: sc.failed,
				Active: sc.active.latency(),
				Total:  sc.total.latency(),
				Errors: sc.errors,
			},
		}
		steps := make([]*stepAcc, 0, len(sc.steps))
		for _, st := range sc.steps {
			if st.seen {
				steps = append(steps, st)
			}
		}
		slices.SortFunc(steps, func(x, y *stepAcc) int {
			return cmp.Or(x.first.Compare(y.first), cmp.Compare(x.seq, y.seq))
		})
		for _, st := range steps {
			sr.Steps = append(sr.Steps, StepStats{
				Name:     st.name,
				Count:    st.count,
				Failed:   st.failed,
				Duration: st.dur.latency(),
				Errors:   st.errors,
				Requests: st.reqs.stats(),
			})
		}
		rep.Scenarios = append(rep.Scenarios, sr)
	}
	return rep
}

// series turns the per-second accumulators into SeriesPoints. It returns nil when nothing was recorded.
func (a *Aggregator) series() []SeriesPoint {
	if a.origin.IsZero() || len(a.secs) == 0 {
		return nil
	}
	pts := make([]SeriesPoint, len(a.secs))
	for i := range a.secs {
		s := &a.secs[i]
		pts[i] = SeriesPoint{
			Second:      i,
			Requests:    s.requests,
			Errors:      s.errors,
			HTTP5xx:     s.http5xx,
			NewConns:    s.newConns,
			ReusedConns: s.reusedConns,
			Latency:     s.lat.latency(),
			DNS:         s.dns.latency(),
			Connect:     s.connect.latency(),
			TLS:         s.tls.latency(),
			Concurrency: a.conc.peak(i),
		}
	}
	return pts
}

// compareScenarios orders scenarios by when they first ran: those with iterations first (by the start of
// their first iteration), then those seen only through steps, then those seen only through requests (by name).
func compareScenarios(x, y *scenarioAcc) int {
	rank := func(s *scenarioAcc) int {
		switch {
		case !s.iterFirst.IsZero():
			return 0
		case !s.stepFirst.IsZero():
			return 1
		}
		return 2
	}
	if c := cmp.Compare(rank(x), rank(y)); c != 0 {
		return c
	}
	switch rank(x) {
	case 0:
		return cmp.Or(x.iterFirst.Compare(y.iterFirst), cmp.Compare(x.seq, y.seq))
	case 1:
		return cmp.Or(x.stepFirst.Compare(y.stepFirst), cmp.Compare(x.seq, y.seq))
	}
	return cmp.Compare(x.name, y.name)
}
