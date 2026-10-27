package report

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
)

func mergeSample(open bool, lat time.Duration, n int, elapsed time.Duration, extraStep string) *Report {
	var reqs []record.Record
	var steps []record.StepRecord
	var iters []record.IterationRecord
	for i := 0; i < n; i++ {
		reqs = append(reqs, record.Record{Scenario: "s", Step: "a", Code: 200, Latency: lat, BytesIn: 10})
		steps = append(steps, record.StepRecord{Scenario: "s", Step: "a", Duration: lat})
		iters = append(iters, record.IterationRecord{Scenario: "s", Duration: lat})
	}
	if extraStep != "" {
		steps = append(steps, record.StepRecord{Scenario: "s", Step: extraStep, Duration: lat, Err: record.ErrTimeout})
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
	// Mean は正確: (10*1 + 30*3) / 4 = 25ms
	if l.Count != 4 || l.Min != 10*time.Millisecond || l.Max != 30*time.Millisecond || l.Mean != 25*time.Millisecond {
		t.Fatalf("latency: %+v", l)
	}
	// P50 はヒストグラムから求める。半分以上が 30ms なので 30ms 付近（重み付き平均の 25ms にはならない）
	if !within(l.P50, 30*time.Millisecond, histogram.Alpha) {
		t.Fatalf("P50 = %v, want ≈30ms", l.P50)
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
	if !reflect.DeepEqual(got.Requests.Latency, a.Requests.Latency) || got.Requests.Count != a.Requests.Count || !got.Open {
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

func within(got, want time.Duration, rel float64) bool {
	return float64(got) >= float64(want)*(1-rel) && float64(got) <= float64(want)*(1+rel)
}

// 分割して結合した結果が、全データを一度に集計した結果に近いこと。
func TestMergeReportsPercentilesMatchWhole(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var all, left, right []record.Record
	for i := 0; i < 20000; i++ {
		// 裾の重い分布 (対数正規風)。A と B で分布の中心をずらす。
		lat := time.Duration(float64(5*time.Millisecond) * (1 + rng.ExpFloat64()*rng.ExpFloat64()*3))
		r := record.Record{Scenario: "s", Step: "a", Code: 200, Latency: lat}
		all = append(all, r)
		if i%3 == 0 {
			r.Latency *= 4
			all[len(all)-1] = r
			right = append(right, r)
		} else {
			left = append(left, r)
		}
	}
	whole := BuildReport(all, nil, nil, time.Second)
	got, err := MergeReports(BuildReport(left, nil, nil, time.Second), BuildReport(right, nil, nil, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	w, g := whole.Requests.Latency, got.Requests.Latency
	for name, pair := range map[string][2]time.Duration{
		"P50": {g.P50, w.P50}, "P90": {g.P90, w.P90}, "P95": {g.P95, w.P95}, "P99": {g.P99, w.P99},
	} {
		if !within(pair[0], pair[1], 2*histogram.Alpha) {
			t.Errorf("%s = %v, want ≈%v (誤差 %.2f%%)", name, pair[0], pair[1], 100*(float64(pair[0])/float64(pair[1])-1))
		}
	}
	if g.Min != w.Min || g.Max != w.Max || g.Count != w.Count {
		t.Errorf("min/max/count: got %+v, want %+v", g, w)
	}
}

func TestMergeReportsElapsedFromStartedAt(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := mergeSample(false, time.Millisecond, 1, 10*time.Second, "")
	b := mergeSample(false, time.Millisecond, 1, 10*time.Second, "")

	// 同時: 5 秒ずらして開始 → 全体は 15 秒
	a.StartedAt, b.StartedAt = base, base.Add(5*time.Second)
	got, _ := MergeReports(a, b)
	if got.Elapsed != 15*time.Second || !got.StartedAt.Equal(base) {
		t.Fatalf("overlap: elapsed = %v, started = %v", got.Elapsed, got.StartedAt)
	}
	// 順次: 1 分後に開始 → 間も含めて 70 秒（RPS が実際より高く出ない）
	b.StartedAt = base.Add(time.Minute)
	got, _ = MergeReports(a, b)
	if got.Elapsed != 70*time.Second {
		t.Fatalf("sequential: elapsed = %v", got.Elapsed)
	}
	// StartedAt が無いものが混じる → Elapsed の最大値
	b.StartedAt = time.Time{}
	got, _ = MergeReports(a, b)
	if got.Elapsed != 10*time.Second || !got.StartedAt.IsZero() {
		t.Fatalf("no start: elapsed = %v, started = %v", got.Elapsed, got.StartedAt)
	}
}

// ヒストグラムの無い version 1 の JSON は、読めて、重み付き平均で結合できる。
func TestMergeReportsVersion1Fallback(t *testing.T) {
	v1 := func(lat time.Duration, n int) *Report {
		rep := mergeSample(false, lat, n, time.Second, "")
		var buf bytes.Buffer
		if err := rep.WriteJSON(&buf); err != nil {
			t.Fatal(err)
		}
		old := strings.Replace(buf.String(), `"version": 2`, `"version": 1`, 1)
		got, err := ReadJSON(strings.NewReader(old))
		if err != nil {
			t.Fatal(err)
		}
		// v1 には無いフィールドを落として、本物の v1 に近づける
		got.Requests.Latency.hist = nil
		return got
	}
	got, err := MergeReports(v1(10*time.Millisecond, 1), v1(30*time.Millisecond, 3))
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests.Latency.P50 != 25*time.Millisecond {
		t.Fatalf("P50 = %v, want 25ms (重み付き平均)", got.Requests.Latency.P50)
	}
}

func TestMergeConnStats(t *testing.T) {
	mk := func(newN, reusedN int) *Report {
		var reqs []record.Record
		for i := 0; i < newN; i++ {
			reqs = append(reqs, record.Record{Code: 200, Latency: time.Millisecond, Conn: record.ConnNew, Connect: time.Millisecond})
		}
		for i := 0; i < reusedN; i++ {
			reqs = append(reqs, record.Record{Code: 200, Latency: time.Millisecond, Conn: record.ConnReused})
		}
		return BuildReport(reqs, nil, nil, time.Second)
	}
	var buf bytes.Buffer
	if err := mk(1, 3).WriteJSON(&buf); err != nil { // JSON を経由しても残る
		t.Fatal(err)
	}
	a, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := MergeReports(a, mk(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	q := got.Requests
	if q.NewConns != 3 || q.ReusedConns != 3 || q.Connect.Count != 3 {
		t.Errorf("merged: %+v", q)
	}
}
