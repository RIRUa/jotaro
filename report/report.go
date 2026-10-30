// Package report aggregates and outputs the results (Report) of a load test.
//
// It aggregates records (record.Record and so on) (BuildReport / FromRaw), writes them as text, JSON or HTML (Write),
// combines several results (MergeReports), and checks them against thresholds (Threshold / Report.Check).
package report

import (
	"fmt"
	"slices"
	"time"

	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
)

// Latency is a distribution of durations.
//
// Percentiles use the nearest-rank method, so each one is an actually observed value.
// A Latency with Count 0 has every other field at 0.
type Latency struct {
	// Count is the number of observations.
	Count int
	Min   time.Duration
	Mean  time.Duration
	// P50 to P99 are the 50th, 90th, 95th and 99th percentiles.
	P50 time.Duration
	P90 time.Duration
	P95 time.Duration
	P99 time.Duration
	Max time.Duration

	// hist は分布のヒストグラム。MergeReports がパーセンタイルを求め直すために使う。
	hist histogram.H
}

// newLatency summarizes ds without modifying it.
func newLatency(ds []time.Duration) Latency {
	return newLatencyOwned(slices.Clone(ds))
}

// newLatencyOwned は newLatency と同じだが、s を並べ替える（コピーしない）。
// 集計の中で作った一時的なスライスを渡して、巨大なコピーを避けるのに使う。
func newLatencyOwned(s []time.Duration) Latency {
	if len(s) == 0 {
		return Latency{}
	}
	slices.Sort(s)

	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	return Latency{
		Count: len(s),
		Min:   s[0],
		Mean:  sum / time.Duration(len(s)),
		P50:   percentile(s, 50),
		P90:   percentile(s, 90),
		P95:   percentile(s, 95),
		P99:   percentile(s, 99),
		Max:   s[len(s)-1],
		hist:  histogram.New(s),
	}
}

// percentile は昇順にソート済みの s から nearest-rank 法で p パーセンタイルを返す。
func percentile(s []time.Duration, p int) time.Duration {
	rank := (p*len(s) + 99) / 100 // ceil(p/100 * n)
	return s[max(rank, 1)-1]
}

// RequestStats is the aggregate per request.
type RequestStats struct {
	Count   int
	Latency Latency // includes requests that ended in a network error
	TTFB    Latency // only requests that received a response
	// DNS / Connect / TLS cover only requests in which that phase actually happened
	// (requests that reused a connection, and TLS over plain http, are not included).
	DNS     Latency
	Connect Latency
	TLS     Latency
	// NewConns / ReusedConns are the number of requests that opened a new connection and that reused one.
	NewConns    int
	ReusedConns int
	// Corrected is the time since the scheduled start (after Coordinated Omission correction).
	// In the Users model it is the same as Latency.
	Corrected Latency

	Status map[string]int         // count per status class such as "2xx"
	Errors map[record.ErrKind]int // breakdown of network errors

	BytesIn  int64
	BytesOut int64
}

// ReuseRate is the share of requests that reused a connection (0 to 1). It is 0 when there is no connection record.
func (q RequestStats) ReuseRate() float64 {
	if n := q.NewConns + q.ReusedConns; n > 0 {
		return float64(q.ReusedConns) / float64(n)
	}
	return 0
}

// StepStats is the aggregate per u.Step.
type StepStats struct {
	Name     string
	Count    int
	Failed   int
	Duration Latency
	Errors   map[record.ErrKind]int // breakdown of step failures
	Requests RequestStats
}

// IterationStats is the aggregate for one run of a scenario.
type IterationStats struct {
	Count  int
	Failed int
	Active Latency // time without Think
	Total  Latency // time including Think
	Errors map[record.ErrKind]int
}

// ScenarioReport is the aggregate per scenario.
type ScenarioReport struct {
	Name       string
	Iterations IterationStats
	Steps      []StepStats // in the order they first ran
}

// Report is the aggregated result of the whole run.
type Report struct {
	// Open reports whether the run used Arrival (the open model).
	Open bool
	// Dropped is the number of arrivals that were not run because of the concurrency cap.
	Dropped int

	// StartedAt is when the run started (UTC). Zero if unknown.
	StartedAt time.Time
	Elapsed   time.Duration
	Requests  RequestStats
	Scenarios []ScenarioReport // in the order they first ran
	// Series is the per-second aggregate since StartedAt. nil when there is no StartedAt.
	Series []SeriesPoint
	// Health is the per-second health of the load generator since StartedAt. Only set when the run was done with Run.
	Health []record.HealthPoint
}

// RPS is the number of requests per second.
func (r *Report) RPS() float64 {
	return perSecond(r.Requests.Count, r.Elapsed)
}

// perSecond is n divided by elapsed in seconds, or 0 when no time has passed.
func perSecond(n int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(n) / elapsed.Seconds()
}

// FromRaw aggregates raw data. The result is the same as the Report returned by Run.
func FromRaw(raw *record.Raw) *Report {
	rep := BuildReport(raw.Requests, raw.Steps, raw.Iterations, raw.Elapsed)
	rep.Open = raw.Open
	rep.Dropped = raw.Dropped
	if !raw.StartedAt.IsZero() {
		rep.StartedAt = wallUTC(raw.StartedAt)
	}
	rep.Health = raw.Health
	return rep
}

// BuildReport aggregates from records.
//
// Scenarios and steps are listed in the order they first started, and each step's requests
// are matched to it by scenario and step name.
// When elapsed is 0, the run time is taken from the first start to the last end among the records.
func BuildReport(reqs []record.Record, steps []record.StepRecord, iters []record.IterationRecord, elapsed time.Duration) *Report {
	if elapsed == 0 {
		elapsed = observedSpan(reqs, iters)
	}
	rep := &Report{StartedAt: observedStart(reqs, steps, iters), Elapsed: elapsed, Requests: requestStats(reqs, nil)}
	rep.Series = buildSeries(rep.StartedAt, reqs, iters)

	type stepKey struct{ scenario, step string }
	var (
		scenarios []*ScenarioReport
		byName    = map[string]*ScenarioReport{}
		stepDurs  = map[stepKey][]time.Duration{}
		stepAt    = map[stepKey]int{}   // sc.Steps 内の位置。append で動くのでポインタでは持たない
		stepReqs  = map[stepKey][]int{} // reqs 内の位置。記録そのものはコピーしない
	)
	scenario := func(name string) *ScenarioReport {
		sc, ok := byName[name]
		if !ok {
			sc = &ScenarioReport{Name: name}
			byName[name] = sc
			scenarios = append(scenarios, sc)
		}
		return sc
	}

	for i, r := range reqs {
		k := stepKey{r.Scenario, r.Step}
		stepReqs[k] = append(stepReqs[k], i)
	}

	// 開始時刻順に並べて、「最初に実行された順」を決める。
	sortedIters := slices.Clone(iters)
	slices.SortStableFunc(sortedIters, func(a, b record.IterationRecord) int { return a.Start.Compare(b.Start) })
	sortedSteps := slices.Clone(steps)
	slices.SortStableFunc(sortedSteps, func(a, b record.StepRecord) int { return a.Start.Compare(b.Start) })

	var (
		activeDurs = map[string][]time.Duration{}
		totalDurs  = map[string][]time.Duration{}
	)
	for _, it := range sortedIters {
		sc := scenario(it.Scenario)
		sc.Iterations.Count++
		if it.Err != record.ErrNone {
			sc.Iterations.Failed++
			sc.Iterations.Errors = count(sc.Iterations.Errors, it.Err)
		}
		activeDurs[it.Scenario] = append(activeDurs[it.Scenario], it.Active())
		totalDurs[it.Scenario] = append(totalDurs[it.Scenario], it.Duration)
	}
	for _, st := range sortedSteps {
		sc := scenario(st.Scenario)
		k := stepKey{st.Scenario, st.Step}
		i, ok := stepAt[k]
		if !ok {
			sc.Steps = append(sc.Steps, StepStats{Name: st.Step})
			i = len(sc.Steps) - 1
			stepAt[k] = i
		}
		ss := &sc.Steps[i]
		ss.Count++
		if st.Err != record.ErrNone {
			ss.Failed++
			ss.Errors = count(ss.Errors, st.Err)
		}
		stepDurs[k] = append(stepDurs[k], st.Duration)
	}
	// ステップの記録が無くリクエストだけあるシナリオも拾う。
	for k := range stepReqs {
		scenario(k.scenario)
	}

	for _, sc := range scenarios {
		sc.Iterations.Active = newLatencyOwned(activeDurs[sc.Name])
		sc.Iterations.Total = newLatencyOwned(totalDurs[sc.Name])
		for i := range sc.Steps {
			ss := &sc.Steps[i]
			k := stepKey{sc.Name, ss.Name}
			ss.Duration = newLatencyOwned(stepDurs[k])
			ss.Requests = requestStats(reqs, stepReqs[k])
		}
		rep.Scenarios = append(rep.Scenarios, *sc)
	}
	return rep
}

// requestStats は reqs のうち at の位置にあるものを集計する。at が nil なら全部。
func requestStats(reqs []record.Record, at []int) RequestStats {
	n := len(reqs)
	if at != nil {
		n = len(at)
	}
	st := RequestStats{Count: n}
	lat, corrected := make([]time.Duration, 0, n), make([]time.Duration, 0, n)
	var ttfb, dns, connect, tlsd []time.Duration
	for j := 0; j < n; j++ {
		i := j
		if at != nil {
			i = at[j]
		}
		r := &reqs[i]
		lat = append(lat, r.Latency)
		corrected = append(corrected, r.Corrected())
		if r.TTFB > 0 {
			ttfb = append(ttfb, r.TTFB)
		}
		for _, p := range []struct {
			d   time.Duration
			dst *[]time.Duration
		}{{r.DNS, &dns}, {r.Connect, &connect}, {r.TLS, &tlsd}} {
			if p.d > 0 {
				*p.dst = append(*p.dst, p.d)
			}
		}
		switch r.Conn {
		case record.ConnNew:
			st.NewConns++
		case record.ConnReused:
			st.ReusedConns++
		}
		if r.Code > 0 {
			if st.Status == nil {
				st.Status = map[string]int{}
			}
			st.Status[fmt.Sprintf("%dxx", r.Code/100)]++
		}
		if r.Err != record.ErrNone {
			st.Errors = count(st.Errors, r.Err)
		}
		st.BytesIn += r.BytesIn
		st.BytesOut += r.BytesOut
	}
	st.Latency = newLatencyOwned(lat)
	st.TTFB = newLatencyOwned(ttfb)
	st.Corrected = newLatencyOwned(corrected)
	st.DNS, st.Connect, st.TLS = newLatencyOwned(dns), newLatencyOwned(connect), newLatencyOwned(tlsd)
	return st
}

// count adds one to the tally of k. The map is created on first use,
// so a stats value with no errors keeps a nil map (and is left out of JSON).
func count(m map[record.ErrKind]int, k record.ErrKind) map[record.ErrKind]int {
	if m == nil {
		m = map[record.ErrKind]int{}
	}
	m[k]++
	return m
}

// observedSpan は記録から実行時間を推定する。
func observedSpan(reqs []record.Record, iters []record.IterationRecord) time.Duration {
	var first, last time.Time
	extend := func(start time.Time, d time.Duration) {
		if first.IsZero() || start.Before(first) {
			first = start
		}
		if end := start.Add(d); end.After(last) {
			last = end
		}
	}
	for _, r := range reqs {
		extend(r.Start, r.Latency)
	}
	for _, it := range iters {
		extend(it.Start, it.Duration)
	}
	if first.IsZero() {
		return 0
	}
	return last.Sub(first)
}

// observedStart は記録の中で一番早い開始時刻を返す。記録が無ければ zero。
func observedStart(reqs []record.Record, steps []record.StepRecord, iters []record.IterationRecord) time.Time {
	var first time.Time
	extend := func(t time.Time) {
		if !t.IsZero() && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	for _, r := range reqs {
		extend(r.Start)
	}
	for _, st := range steps {
		extend(st.Start)
	}
	for _, it := range iters {
		extend(it.Start)
	}
	return wallUTC(first)
}

// wallUTC は単調時計とタイムゾーンを落とす。JSON を往復しても同じ値になる。
func wallUTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.Round(0).UTC()
}
