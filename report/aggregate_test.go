package report

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// event is one thing the Aggregator is told, at the moment it would happen in a real run.
type event struct {
	at   time.Time
	end  bool // ends sort before starts at the same instant, like the sweep in buildSeries
	call func(*Aggregator)
}

// genRun produces a random run of three scenarios and the records it would leave.
func genRun(seed uint64) (origin time.Time, reqs []record.Record, steps []record.StepRecord, iters []record.IterationRecord, events []event) {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	origin = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scenarios := []string{"checkout", "browse", "admin"}
	stepNames := []string{"login", "list", "order"}
	errKinds := []record.ErrKind{record.ErrNone, record.ErrNone, record.ErrNone, record.ErrTimeout, record.ErrExpect}
	codes := []int{200, 200, 201, 404, 500, 503, 0}

	for u := 0; u < 400; u++ {
		sc := scenarios[rng.IntN(len(scenarios))]
		itStart := origin.Add(time.Duration(rng.IntN(6000)) * time.Millisecond)
		cur := itStart
		var think time.Duration
		for _, name := range stepNames[:1+rng.IntN(len(stepNames))] {
			stStart := cur
			for k := 0; k < 1+rng.IntN(3); k++ {
				lat := time.Duration(1+rng.IntN(300_000)) * time.Microsecond
				r := record.Record{
					Scenario: sc, Step: name, UserID: u, Method: "GET", URL: "/" + name,
					Code: codes[rng.IntN(len(codes))], Err: errKinds[rng.IntN(len(errKinds))],
					Start: cur, Scheduled: cur.Add(-time.Duration(rng.IntN(5)) * time.Millisecond), Latency: lat,
					BytesIn: int64(rng.IntN(5000)), BytesOut: int64(rng.IntN(500)),
				}
				if rng.IntN(2) == 0 {
					r.Conn = record.ConnReused
				} else {
					r.Conn = record.ConnNew
					r.DNS = time.Duration(1+rng.IntN(5000)) * time.Microsecond
					r.Connect = time.Duration(1+rng.IntN(5000)) * time.Microsecond
					r.TLS = time.Duration(1+rng.IntN(9000)) * time.Microsecond
				}
				if r.Code > 0 {
					r.TTFB = lat / 2
				}
				reqs = append(reqs, r)
				events = append(events, event{at: cur.Add(lat), end: true, call: func(a *Aggregator) { a.ObserveRequest(r) }})
				cur = cur.Add(lat)
			}
			st := record.StepRecord{Scenario: sc, Step: name, UserID: u, Start: stStart, Duration: cur.Sub(stStart), Err: errKinds[rng.IntN(len(errKinds))]}
			steps = append(steps, st)
			events = append(events, event{at: cur, end: true, call: func(a *Aggregator) { a.ObserveStep(st) }})
			t := time.Duration(rng.IntN(20)) * time.Millisecond
			think += t
			cur = cur.Add(t)
		}
		it := record.IterationRecord{Scenario: sc, UserID: u, Start: itStart, Duration: cur.Sub(itStart), Think: think, Err: errKinds[rng.IntN(len(errKinds))]}
		iters = append(iters, it)
		events = append(events,
			event{at: itStart, call: func(a *Aggregator) { a.ObserveIterationStart(it.Start) }},
			event{at: cur, end: true, call: func(a *Aggregator) { a.ObserveIteration(it) }},
		)
	}
	slices.SortStableFunc(events, func(x, y event) int {
		if c := x.at.Compare(y.at); c != 0 {
			return c
		}
		if x.end != y.end {
			if x.end {
				return -1
			}
			return 1
		}
		return 0
	})
	return origin, reqs, steps, iters, events
}

// near reports whether got is within the 1% relative error of the histogram (plus 1ns for rounding) of want.
func near(got, want time.Duration) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) <= 0.011*float64(want)+1
}

// requireLatency checks that got is the approximation of want: exact count, min, max and mean, percentiles within 1%.
func requireLatency(t *testing.T, name string, got, want Latency) {
	t.Helper()
	if got.Count != want.Count || got.Min != want.Min || got.Max != want.Max || got.Mean != want.Mean {
		t.Errorf("%s: got count=%d min=%v max=%v mean=%v, want count=%d min=%v max=%v mean=%v",
			name, got.Count, got.Min, got.Max, got.Mean, want.Count, want.Min, want.Max, want.Mean)
	}
	for _, p := range []struct {
		n         string
		got, want time.Duration
	}{{"p50", got.P50, want.P50}, {"p90", got.P90, want.P90}, {"p95", got.P95, want.P95}, {"p99", got.P99, want.P99}} {
		if !near(p.got, p.want) {
			t.Errorf("%s %s: got %v, want %v (within 1%%)", name, p.n, p.got, p.want)
		}
	}
	if got.Count > 0 && got.hist.Total() != got.Count {
		t.Errorf("%s: histogram holds %d observations, want %d (MergeReports needs them to match)", name, got.hist.Total(), got.Count)
	}
}

func requireRequestStats(t *testing.T, name string, got, want RequestStats) {
	t.Helper()
	if got.Count != want.Count || got.NewConns != want.NewConns || got.ReusedConns != want.ReusedConns ||
		got.BytesIn != want.BytesIn || got.BytesOut != want.BytesOut ||
		!reflect.DeepEqual(got.Status, want.Status) || !reflect.DeepEqual(got.Errors, want.Errors) {
		t.Errorf("%s: counters differ:\n got  %+v\n want %+v", name, got, want)
	}
	requireLatency(t, name+" latency", got.Latency, want.Latency)
	requireLatency(t, name+" corrected", got.Corrected, want.Corrected)
	requireLatency(t, name+" ttfb", got.TTFB, want.TTFB)
	requireLatency(t, name+" dns", got.DNS, want.DNS)
	requireLatency(t, name+" connect", got.Connect, want.Connect)
	requireLatency(t, name+" tls", got.TLS, want.TLS)
}

// The Aggregator must agree with BuildReport on everything except the 1% approximation of percentiles.
func TestAggregatorMatchesBuildReport(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		_, reqs, steps, iters, events := genRun(seed)
		want := BuildReport(reqs, steps, iters, 0)

		// BuildReport counts seconds from the earliest record; in a real run the origin is the start of the run.
		a := NewAggregator(want.StartedAt)
		for _, e := range events {
			e.call(a)
		}
		got := a.Report(want.Elapsed)

		if !got.StartedAt.Equal(want.StartedAt) || got.Elapsed != want.Elapsed {
			t.Errorf("seed %d: started %v elapsed %v, want %v %v", seed, got.StartedAt, got.Elapsed, want.StartedAt, want.Elapsed)
		}
		requireRequestStats(t, "all", got.Requests, want.Requests)

		if len(got.Scenarios) != len(want.Scenarios) {
			t.Fatalf("seed %d: %d scenarios, want %d", seed, len(got.Scenarios), len(want.Scenarios))
		}
		for i, ws := range want.Scenarios {
			gs := got.Scenarios[i]
			if gs.Name != ws.Name {
				t.Errorf("seed %d: scenario %d is %q, want %q", seed, i, gs.Name, ws.Name)
				continue
			}
			if gs.Iterations.Count != ws.Iterations.Count || gs.Iterations.Failed != ws.Iterations.Failed ||
				!reflect.DeepEqual(gs.Iterations.Errors, ws.Iterations.Errors) {
				t.Errorf("seed %d: %s iterations %+v, want %+v", seed, ws.Name, gs.Iterations, ws.Iterations)
			}
			requireLatency(t, ws.Name+" active", gs.Iterations.Active, ws.Iterations.Active)
			requireLatency(t, ws.Name+" total", gs.Iterations.Total, ws.Iterations.Total)
			if len(gs.Steps) != len(ws.Steps) {
				t.Fatalf("seed %d: %s has %d steps, want %d", seed, ws.Name, len(gs.Steps), len(ws.Steps))
			}
			for j, wst := range ws.Steps {
				gst := gs.Steps[j]
				name := ws.Name + "/" + wst.Name
				if gst.Name != wst.Name || gst.Count != wst.Count || gst.Failed != wst.Failed || !reflect.DeepEqual(gst.Errors, wst.Errors) {
					t.Errorf("seed %d: %s step %+v, want %+v", seed, name, gst, wst)
				}
				requireLatency(t, name+" duration", gst.Duration, wst.Duration)
				requireRequestStats(t, name, gst.Requests, wst.Requests)
			}
		}

		if len(got.Series) != len(want.Series) {
			t.Fatalf("seed %d: series has %d points, want %d", seed, len(got.Series), len(want.Series))
		}
		for i, wp := range want.Series {
			gp := got.Series[i]
			if gp.Second != wp.Second || gp.Requests != wp.Requests || gp.Errors != wp.Errors || gp.HTTP5xx != wp.HTTP5xx ||
				gp.NewConns != wp.NewConns || gp.ReusedConns != wp.ReusedConns || gp.Concurrency != wp.Concurrency {
				t.Errorf("seed %d: series[%d] = %+v, want %+v", seed, i, gp, wp)
			}
			requireLatency(t, "series latency", gp.Latency, wp.Latency)
			requireLatency(t, "series dns", gp.DNS, wp.DNS)
			requireLatency(t, "series connect", gp.Connect, wp.Connect)
			requireLatency(t, "series tls", gp.TLS, wp.TLS)
		}
	}
}

// Memory must not depend on the number of requests: only the histograms' buckets are kept.
func TestAggregatorKeepsNoRecords(t *testing.T) {
	a := NewAggregator(time.Now())
	r := record.Record{Scenario: "s", Step: "x", Code: 200, Start: a.origin, Latency: 5 * time.Millisecond}
	for i := 0; i < 100_000; i++ {
		r.Latency = time.Duration(1+i%1000) * time.Millisecond
		a.ObserveRequest(r)
	}
	rep := a.Report(time.Second)
	if rep.Requests.Count != 100_000 {
		t.Fatalf("count = %d, want 100000", rep.Requests.Count)
	}
	if n := len(rep.Requests.Latency.hist); n > 1000 {
		t.Errorf("histogram has %d buckets for 1000 distinct values, want a bounded number", n)
	}
}

func TestAggregatorEmpty(t *testing.T) {
	rep := NewAggregator(time.Now()).Report(0)
	if rep.Requests.Count != 0 || rep.Series != nil || len(rep.Scenarios) != 0 {
		t.Errorf("empty aggregator gave %+v", rep)
	}
}

// Scenarios first seen only through steps or requests are still listed, after those with iterations.
func TestAggregatorScenarioOrder(t *testing.T) {
	origin := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := NewAggregator(origin)
	at := func(ms int) time.Time { return origin.Add(time.Duration(ms) * time.Millisecond) }

	a.ObserveRequest(record.Record{Scenario: "zeta", Step: "z", Start: at(0), Latency: time.Millisecond})  // requests only
	a.ObserveRequest(record.Record{Scenario: "alpha", Step: "z", Start: at(0), Latency: time.Millisecond}) // requests only
	a.ObserveStep(record.StepRecord{Scenario: "stepOnly", Step: "s", Start: at(5), Duration: time.Millisecond})
	a.ObserveIteration(record.IterationRecord{Scenario: "late", Start: at(20), Duration: time.Millisecond})
	a.ObserveIteration(record.IterationRecord{Scenario: "early", Start: at(10), Duration: time.Millisecond})
	a.ObserveStep(record.StepRecord{Scenario: "early", Step: "b", Start: at(12), Duration: time.Millisecond})
	a.ObserveStep(record.StepRecord{Scenario: "early", Step: "a", Start: at(11), Duration: time.Millisecond})

	var names []string
	for _, sc := range a.Report(time.Second).Scenarios {
		names = append(names, sc.Name)
	}
	if want := []string{"early", "late", "stepOnly", "alpha", "zeta"}; !slices.Equal(names, want) {
		t.Errorf("scenario order = %v, want %v", names, want)
	}

	early := a.Report(time.Second).Scenarios[0]
	if len(early.Steps) != 2 || early.Steps[0].Name != "a" || early.Steps[1].Name != "b" {
		t.Errorf("steps of early = %+v, want a then b", early.Steps)
	}
	// A step that only has requests (no step record) is not listed, but its requests count in the total.
	if got := a.Report(time.Second).Requests.Count; got != 2 {
		t.Errorf("total requests = %d, want 2", got)
	}
}

func TestConcurrency(t *testing.T) {
	var c concurrency
	c.change(0, +1)
	c.change(0, +1) // 2 during second 0
	c.change(1, -1) // second 1 starts at 2, then drops to 1
	c.change(3, +1) // seconds 2 and 3 start at 1; second 3 peaks at 2
	c.change(2, -1) // late event: counted in the latest second (3)
	for sec, want := range []int{2, 2, 1, 2} {
		if got := c.peak(sec); got != want {
			t.Errorf("peak(%d) = %d, want %d", sec, got, want)
		}
	}
	if got := c.peak(10); got != 1 {
		t.Errorf("peak beyond the last event = %d, want the current value 1", got)
	}
}

func TestAggregatorConcurrentUse(t *testing.T) {
	a := NewAggregator(time.Now())
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				a.ObserveIterationStart(time.Now())
				a.ObserveRequest(record.Record{Scenario: "s", Step: "x", Code: 200, Start: time.Now(), Latency: time.Millisecond})
				a.ObserveStep(record.StepRecord{Scenario: "s", Step: "x", Start: time.Now(), Duration: time.Millisecond})
				a.ObserveIteration(record.IterationRecord{Scenario: "s", Start: time.Now(), Duration: time.Millisecond})
			}
		}()
	}
	wg.Wait()
	if got := a.Report(time.Second).Requests.Count; got != 8000 {
		t.Errorf("requests = %d, want 8000", got)
	}
}

func BenchmarkAggregatorObserveRequestParallel(b *testing.B) {
	a := NewAggregator(time.Now())
	r := record.Record{Scenario: "checkout", Step: "order", Method: "POST", URL: "/orders", Code: 201, Conn: record.ConnReused, Start: a.origin, Latency: 5 * time.Millisecond, TTFB: 3 * time.Millisecond}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			a.ObserveRequest(r)
		}
	})
}
