package report

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// randomMergeReport builds a report from random records. The run starts at start+startOffset (zero start = no StartedAt),
// lasts up to maxSeconds, and each step name is drawn from the given names, so reports differ in length, offset and steps.
func randomMergeReport(rng *rand.Rand, start time.Time, startOffset time.Duration, maxSeconds int, steps []string) *Report {
	seconds := 1 + rng.IntN(maxSeconds)
	var (
		reqs  []record.Record
		recs  []record.StepRecord
		iters []record.IterationRecord
	)
	at := start.Add(startOffset)
	for s := 0; s < seconds; s++ {
		if rng.IntN(5) == 0 {
			continue // a second without requests becomes a gap in the series
		}
		for i := rng.IntN(6); i >= 0; i-- {
			step := steps[rng.IntN(len(steps))]
			t := at.Add(time.Duration(s)*time.Second + time.Duration(rng.IntN(1000))*time.Millisecond)
			r := record.Record{
				Scenario: "s", Step: step, Start: t, Code: 200 + 100*rng.IntN(4),
				Latency: time.Duration(1+rng.IntN(500)) * time.Millisecond,
				Conn:    []record.ConnKind{record.ConnNew, record.ConnReused, ""}[rng.IntN(3)],
			}
			if rng.IntN(7) == 0 {
				r.Err = record.ErrTimeout
				r.Code = 0
			}
			if r.Conn == record.ConnNew {
				r.DNS, r.Connect, r.TLS = time.Duration(1+rng.IntN(5))*time.Millisecond, time.Duration(1+rng.IntN(9))*time.Millisecond, time.Duration(1+rng.IntN(20))*time.Millisecond
			}
			reqs = append(reqs, r)
			recs = append(recs, record.StepRecord{Scenario: "s", Step: step, Duration: r.Latency})
		}
		iters = append(iters, record.IterationRecord{Scenario: "s", Start: at.Add(time.Duration(s) * time.Second), Duration: time.Duration(1+rng.IntN(3000)) * time.Millisecond, UserID: rng.IntN(4), Iteration: s})
	}
	rep := BuildReport(reqs, recs, iters, time.Duration(seconds)*time.Second)
	if startOffset >= 0 {
		rep.StartedAt = at
	}
	for s := 0; s < seconds; s++ {
		if rng.IntN(4) == 0 {
			continue
		}
		rep.Health = append(rep.Health, record.HealthPoint{Second: s, Goroutines: rng.IntN(1000), CPU: float64(rng.IntN(101))/100 - 0.01})
	}
	return rep
}

// cloneMergeReport copies the series and health slices of rep, so a test can check that the merge leaves its inputs alone.
func cloneMergeReport(rep *Report) *Report {
	c := *rep
	c.Series = slices.Clone(rep.Series)
	c.Health = slices.Clone(rep.Health)
	return &c
}

func jsonOf(t *testing.T, rep *Report) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// requireSameAsReference checks that MergeReports gives the Report that folding the series one report at a time
// (the implementation before the fix) gives, down to the bytes of its JSON.
func requireSameAsReference(t *testing.T, reps ...*Report) {
	t.Helper()
	before := make([]*Report, len(reps))
	for i, r := range reps {
		before[i] = cloneMergeReport(r)
	}
	got, err := MergeReports(reps...)
	if err != nil {
		t.Fatal(err)
	}
	want := *got
	want.Series, want.Health = refMergeSeriesHealth(reps)
	if !reflect.DeepEqual(got.Series, want.Series) {
		t.Fatalf("series differ:\n got %+v\nwant %+v", got.Series, want.Series)
	}
	if !reflect.DeepEqual(got.Health, want.Health) {
		t.Fatalf("health differ:\n got %+v\nwant %+v", got.Health, want.Health)
	}
	if (got.Series == nil) != (want.Series == nil) || (got.Health == nil) != (want.Health == nil) {
		t.Fatalf("nil-ness differs: series %v/%v, health %v/%v", got.Series == nil, want.Series == nil, got.Health == nil, want.Health == nil)
	}
	if a, b := jsonOf(t, got), jsonOf(t, &want); !bytes.Equal(a, b) {
		t.Fatalf("JSON differs:\n got %s\nwant %s", a, b)
	}
	for i, r := range reps {
		if !reflect.DeepEqual(r, before[i]) {
			t.Fatalf("report #%d was modified by the merge", i+1)
		}
	}
}

// TestMergeReportsMatchesReference compares the merge with the pre-fix implementation on random reports:
// different lengths, different start offsets, a missing StartedAt, gaps, and steps present in only one report.
func TestMergeReportsMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 87))
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stepSets := [][]string{{"a"}, {"a", "b"}, {"b", "c"}, {"a", "b", "c", "d"}}
	for n := 0; n < 60; n++ {
		count := 1 + rng.IntN(4)
		reps := make([]*Report, count)
		noStart := rng.IntN(5) == 0 // one report without StartedAt aligns every start
		for i := range reps {
			offset := time.Duration(rng.IntN(40_000)) * time.Millisecond
			if noStart && i == count-1 {
				offset = -1
			}
			reps[i] = randomMergeReport(rng, start, offset, 1+rng.IntN(60), stepSets[rng.IntN(len(stepSets))])
		}
		requireSameAsReference(t, reps...)
	}
}

func TestMergeReportsDistinctShapes(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 87))
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	long := randomMergeReport(rng, start, 0, 300, []string{"a", "b"})
	short := randomMergeReport(rng, start, 90*time.Second, 5, []string{"c"})
	late := randomMergeReport(rng, start, 10*time.Minute, 50, []string{"a"}) // starts after the other two ended: a gap between them
	requireSameAsReference(t, long, short)
	requireSameAsReference(t, short, long)
	requireSameAsReference(t, long, short, late)
	requireSameAsReference(t, late, long)
	requireSameAsReference(t, long) // a single report
	requireSameAsReference(t, long, long, long)
}

// TestMergeReportsEmptySeries keeps the result nil (not an empty slice) when no report has points.
func TestMergeReportsEmptySeries(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &Report{StartedAt: start, Elapsed: time.Second, Series: []SeriesPoint{}, Health: []record.HealthPoint{}}
	b := &Report{StartedAt: start, Elapsed: time.Second}
	requireSameAsReference(t, a, b)
	got, err := MergeReports(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Series != nil || got.Health != nil {
		t.Fatalf("Series = %#v, Health = %#v, want nil", got.Series, got.Health)
	}
	requireSameAsReference(t, b, a)
}

// TestMergeSeriesIrregular covers input that a run never writes but a hand-edited or corrupt JSON can hold:
// points out of order, the same second twice in one report, and seconds far apart.
func TestMergeSeriesIrregular(t *testing.T) {
	ms := time.Millisecond
	pt := func(sec, n int, d time.Duration) SeriesPoint {
		return SeriesPoint{Second: sec, Requests: n, Concurrency: n, Latency: newLatency([]time.Duration{d})}
	}
	cases := []struct {
		name  string
		parts [][]SeriesPoint
		offs  []int
	}{
		{"unsorted", [][]SeriesPoint{{pt(5, 1, ms), pt(2, 2, 2*ms), pt(9, 3, 3*ms)}, {pt(2, 4, 4*ms), pt(0, 5, 5*ms)}}, []int{0, 3}},
		{"duplicate in one report", [][]SeriesPoint{{pt(1, 1, ms), pt(1, 2, 2*ms), pt(0, 3, 3*ms), pt(1, 4, 4*ms)}, {pt(1, 5, 5*ms)}}, []int{0, 0}},
		{"far apart", [][]SeriesPoint{{pt(0, 1, ms), pt(3_000_000_000, 2, 2*ms)}, {pt(3_000_000_000, 3, 3*ms), pt(7, 4, 4*ms)}}, []int{0, 0}},
		{"negative seconds", [][]SeriesPoint{{pt(-2, 1, ms), pt(0, 2, 2*ms)}, {pt(-2, 3, 3*ms)}}, []int{0, 2}},
		{"empty first", [][]SeriesPoint{nil, {pt(1, 1, ms)}, {}}, []int{0, 0, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want []SeriesPoint
			for i, p := range tc.parts {
				want = refMergeSeries(want, p, tc.offs[i])
			}
			got := mergeSeries(tc.parts, tc.offs)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("series differ:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestMergeHealthIrregular(t *testing.T) {
	parts := [][]record.HealthPoint{
		{{Second: 4, Goroutines: 1, CPU: 0.5}, {Second: 1, Goroutines: 2, CPU: -1}, {Second: 4, Goroutines: 3, CPU: 0.1}},
		{{Second: 1, Goroutines: 4, CPU: 0.3}, {Second: 2_000_000_000, Goroutines: 5, CPU: 0.2}},
	}
	offs := []int{0, 3}
	var want []record.HealthPoint
	for i, p := range parts {
		want = refMergeHealth(want, p, offs[i])
	}
	if got := mergeHealth(parts, offs); !reflect.DeepEqual(got, want) {
		t.Fatalf("health differ:\n got %+v\nwant %+v", got, want)
	}
}

// TestMergeSeriesFoldOrder pins the order in which points of one second are folded: Mean is rounded at every step,
// so folding 0ns, 0ns, 1ns gives 0ns while folding 1ns, 0ns, 0ns gives 1ns.
func TestMergeSeriesFoldOrder(t *testing.T) {
	pt := func(d time.Duration) SeriesPoint {
		return SeriesPoint{Second: 3, Latency: newLatency([]time.Duration{d})}
	}
	for _, tc := range []struct {
		name string
		part []SeriesPoint
		want time.Duration
	}{
		{"0,0,1", []SeriesPoint{pt(0), pt(0), pt(1)}, 0},
		{"1,0,0", []SeriesPoint{pt(1), pt(0), pt(0)}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeSeries([][]SeriesPoint{tc.part}, []int{0})
			if len(got) != 1 || got[0].Latency.Mean != tc.want {
				t.Fatalf("got %+v, want one point with Mean %v", got, tc.want)
			}
			if ref := refMergeSeries(nil, tc.part, 0); !reflect.DeepEqual(got, ref) {
				t.Fatalf("differs from reference:\n got %+v\nwant %+v", got, ref)
			}
		})
	}
}
