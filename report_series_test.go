package jotaro

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

func seriesRecords(t0 time.Time) ([]Record, []IterationRecord) {
	ms := time.Millisecond
	reqs := []Record{
		{Start: t0, Latency: 10 * ms, Code: 200},
		{Start: t0.Add(500 * ms), Latency: 30 * ms, Err: ErrTimeout},
		{Start: t0.Add(2100 * ms), Latency: 20 * ms, Code: 503}, // 1 秒目は空
	}
	iters := []IterationRecord{
		{Start: t0, Duration: 1500 * ms},
		{Start: t0.Add(200 * ms), Duration: 600 * ms},
		{Start: t0.Add(1500 * ms), Duration: 700 * ms}, // 前のイテレーションの終了と同時
	}
	return reqs, iters
}

func TestBuildSeries(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs, iters := seriesRecords(t0)
	rep := BuildReport(reqs, nil, iters, 0)

	if len(rep.Series) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(rep.Series), rep.Series)
	}
	s0, s1, s2 := rep.Series[0], rep.Series[1], rep.Series[2]
	if s0.Second != 0 || s0.Requests != 2 || s0.Errors != 1 || s0.Latency.Count != 2 || s0.Latency.Max != 30*time.Millisecond {
		t.Errorf("s0 = %+v", s0)
	}
	if s0.Concurrency != 2 {
		t.Errorf("s0.Concurrency = %d, want 2", s0.Concurrency)
	}
	// 1 秒目: 0 秒開始のイテレーションが 1.5 秒まで続き、1.5 秒に次が始まる（重ねない）。
	if s1.Requests != 0 || s1.Concurrency != 1 {
		t.Errorf("s1 = %+v", s1)
	}
	if s0.HTTP5xx != 0 || s2.HTTP5xx != 1 || s2.Errors != 0 {
		t.Errorf("5xx: s0 = %+v, s2 = %+v", s0, s2)
	}
	if s2.Requests != 1 || s2.Concurrency != 1 {
		t.Errorf("s2 = %+v", s2)
	}
}

func TestBuildSeriesNoStart(t *testing.T) {
	rep := BuildReport([]Record{{Latency: time.Millisecond}}, nil, nil, time.Second)
	if rep.Series != nil {
		t.Errorf("Series = %+v, want nil", rep.Series)
	}
}

func TestSeriesJSONRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs, iters := seriesRecords(t0)
	rep := BuildReport(reqs, nil, iters, 0)

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Series, rep.Series) {
		t.Errorf("series differs:\n got %+v\nwant %+v", got.Series, rep.Series)
	}
}

func TestMergeSeriesOffset(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs, iters := seriesRecords(t0)
	a := BuildReport(reqs, nil, iters, 0)
	reqs2, iters2 := seriesRecords(t0.Add(2 * time.Second)) // 2 秒遅れて開始
	b := BuildReport(reqs2, nil, iters2, 0)

	got, err := MergeReports(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != 5 {
		t.Fatalf("len = %d, want 5: %+v", len(got.Series), got.Series)
	}
	// 2 秒目は a の 2 秒目と b の 0 秒目が重なる。
	s2 := got.Series[2]
	if s2.Requests != 1+2 || s2.Errors != 1 || s2.HTTP5xx != 1 || s2.Latency.Count != 3 || s2.Concurrency != 1+2 {
		t.Errorf("s2 = %+v", s2)
	}
	if got.Series[4].Requests != 1 {
		t.Errorf("s4 = %+v", got.Series[4])
	}
}
