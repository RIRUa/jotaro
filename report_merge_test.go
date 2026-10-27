package jotaro

import (
	"testing"
	"time"
)

func mergeSample(open bool, lat time.Duration, n int, elapsed time.Duration, extraStep string) *Report {
	var reqs []Record
	var steps []StepRecord
	var iters []IterationRecord
	for i := 0; i < n; i++ {
		reqs = append(reqs, Record{Scenario: "s", Step: "a", Code: 200, Latency: lat, BytesIn: 10})
		steps = append(steps, StepRecord{Scenario: "s", Step: "a", Duration: lat})
		iters = append(iters, IterationRecord{Scenario: "s", Duration: lat})
	}
	if extraStep != "" {
		steps = append(steps, StepRecord{Scenario: "s", Step: extraStep, Duration: lat, Err: ErrTimeout})
	}
	rep := BuildReport(reqs, steps, iters, elapsed)
	rep.Open = open
	rep.Dropped = 1
	return rep
}

func TestMergeReports(t *testing.T) {
	a := mergeSample(false, 10*time.Millisecond, 1, 10*time.Second, "")
	b := mergeSample(false, 30*time.Millisecond, 3, 12*time.Second, "b")

	got, err := MergeReports(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests.Count != 4 || got.Dropped != 2 || got.Elapsed != 12*time.Second {
		t.Fatalf("summary: %+v", got)
	}
	if got.Requests.BytesIn != 40 || got.Requests.Status["2xx"] != 4 {
		t.Fatalf("requests: %+v", got.Requests)
	}
	l := got.Requests.Latency
	// 件数で重み付け: (10*1 + 30*3) / 4 = 25ms
	if l.Count != 4 || l.Min != 10*time.Millisecond || l.Max != 30*time.Millisecond || l.P50 != 25*time.Millisecond {
		t.Fatalf("latency: %+v", l)
	}
	if len(got.Scenarios) != 1 || got.Scenarios[0].Iterations.Count != 4 {
		t.Fatalf("scenarios: %+v", got.Scenarios)
	}
	steps := got.Scenarios[0].Steps
	if len(steps) != 2 || steps[0].Name != "a" || steps[0].Count != 4 || steps[1].Name != "b" || steps[1].Failed != 1 {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestMergeReportsSingleIsIdentity(t *testing.T) {
	a := mergeSample(true, 10*time.Millisecond, 2, time.Second, "")
	got, err := MergeReports(a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests.Latency != a.Requests.Latency || got.Requests.Count != a.Requests.Count || !got.Open {
		t.Fatalf("got %+v, want %+v", got, a)
	}
}

func TestMergeReportsErrors(t *testing.T) {
	if _, err := MergeReports(); err == nil {
		t.Error("空はエラーにする")
	}
	a := mergeSample(false, time.Millisecond, 1, time.Second, "")
	b := mergeSample(true, time.Millisecond, 1, time.Second, "")
	if _, err := MergeReports(a, b); err == nil {
		t.Error("Open が違うものはエラーにする")
	}
}
