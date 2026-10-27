package jotaro

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func seriesRecords(t0 time.Time) ([]record.Record, []record.IterationRecord) {
	ms := time.Millisecond
	reqs := []record.Record{
		{Start: t0, Latency: 10 * ms, Code: 200},
		{Start: t0.Add(500 * ms), Latency: 30 * ms, Err: record.ErrTimeout},
		{Start: t0.Add(2100 * ms), Latency: 20 * ms, Code: 503}, // 1 秒目は空
	}
	iters := []record.IterationRecord{
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
	rep := BuildReport([]record.Record{{Latency: time.Millisecond}}, nil, nil, time.Second)
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

func TestBuildSeriesConns(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms := time.Millisecond
	reqs := []record.Record{
		{Start: t0, Latency: 10 * ms, Code: 200, Conn: record.ConnNew},
		{Start: t0.Add(300 * ms), Latency: 10 * ms, Code: 200, Conn: record.ConnReused},
		{Start: t0.Add(600 * ms), Latency: 10 * ms, Code: 200, Conn: record.ConnReused},
		{Start: t0.Add(700 * ms), Latency: 10 * ms, Err: record.ErrDNS}, // 接続を得る前に失敗
		{Start: t0.Add(1200 * ms), Latency: 10 * ms, Code: 200, Conn: record.ConnNew},
	}
	rep := BuildReport(reqs, nil, nil, 0)

	if len(rep.Series) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(rep.Series), rep.Series)
	}
	s0, s1 := rep.Series[0], rep.Series[1]
	if s0.Requests != 4 || s0.NewConns != 1 || s0.ReusedConns != 2 {
		t.Errorf("s0 = %+v", s0)
	}
	if s1.NewConns != 1 || s1.ReusedConns != 0 {
		t.Errorf("s1 = %+v", s1)
	}
}

func TestMergeSeriesConns(t *testing.T) {
	a := []SeriesPoint{{Second: 0, NewConns: 1, ReusedConns: 2}, {Second: 1, NewConns: 3}}
	b := []SeriesPoint{{Second: 0, NewConns: 4, ReusedConns: 5}}

	got := mergeSeries(a, b, 1) // b を 1 秒ずらす → a の 1 秒目と重なる
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].NewConns != 1 || got[0].ReusedConns != 2 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].NewConns != 3+4 || got[1].ReusedConns != 5 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestBuildSeriesConnPhases(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms := time.Millisecond
	reqs := []record.Record{
		{Start: t0, Latency: 50 * ms, Conn: record.ConnNew, DNS: 5 * ms, Connect: 10 * ms, TLS: 20 * ms},
		{Start: t0.Add(100 * ms), Latency: 10 * ms, Conn: record.ConnReused},                              // 再利用は 0
		{Start: t0.Add(200 * ms), Latency: 40 * ms, Conn: record.ConnNew, DNS: 15 * ms, Connect: 30 * ms}, // http は TLS なし
		{Start: t0.Add(1100 * ms), Latency: 10 * ms, Conn: record.ConnReused},
	}
	rep := BuildReport(reqs, nil, nil, 0)

	s0, s1 := rep.Series[0], rep.Series[1]
	if s0.DNS.Count != 2 || s0.DNS.Max != 15*ms || s0.DNS.Min != 5*ms {
		t.Errorf("s0.DNS = %+v", s0.DNS)
	}
	if s0.Connect.Count != 2 || s0.Connect.Max != 30*ms {
		t.Errorf("s0.Connect = %+v", s0.Connect)
	}
	if s0.TLS.Count != 1 || s0.TLS.Max != 20*ms {
		t.Errorf("s0.TLS = %+v", s0.TLS)
	}
	if s1.DNS.Count != 0 || s1.Connect.Count != 0 || s1.TLS.Count != 0 {
		t.Errorf("再利用だけの秒は空のはず: %+v", s1)
	}
}

func TestMergeSeriesConnPhases(t *testing.T) {
	ms := time.Millisecond
	pt := func(sec int, d time.Duration) SeriesPoint {
		return SeriesPoint{Second: sec, DNS: newLatency([]time.Duration{d}), Connect: newLatency([]time.Duration{2 * d}), TLS: newLatency([]time.Duration{3 * d})}
	}
	got := mergeSeries([]SeriesPoint{pt(0, 10*ms)}, []SeriesPoint{pt(0, 30*ms)}, 0)

	if len(got) != 1 {
		t.Fatalf("len = %d: %+v", len(got), got)
	}
	p := got[0]
	if p.DNS.Count != 2 || p.DNS.Min != 10*ms || p.DNS.Max != 30*ms {
		t.Errorf("DNS = %+v", p.DNS)
	}
	if p.Connect.Count != 2 || p.Connect.Max != 60*ms || p.TLS.Count != 2 || p.TLS.Max != 90*ms {
		t.Errorf("Connect = %+v, TLS = %+v", p.Connect, p.TLS)
	}
}

func TestSeriesConnJSONRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms := time.Millisecond
	reqs := []record.Record{
		{Start: t0, Latency: 50 * ms, Code: 200, Conn: record.ConnNew, DNS: 5 * ms, Connect: 10 * ms, TLS: 20 * ms},
		{Start: t0.Add(100 * ms), Latency: 10 * ms, Code: 200, Conn: record.ConnReused},
		{Start: t0.Add(1100 * ms), Latency: 12 * ms, Code: 200, Conn: record.ConnReused},
	}
	rep := BuildReport(reqs, nil, nil, 0)

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
	if s0 := got.Series[0]; s0.NewConns != 1 || s0.ReusedConns != 1 || s0.TLS.Max != 20*ms {
		t.Errorf("s0 = %+v", s0)
	}
}

// 接続内訳の無い（後から足す前の）series も読める。
func TestReadJSONSeriesWithoutConns(t *testing.T) {
	const in = `{"version":3,"started_at":"2026-01-01T00:00:00Z","elapsed_ns":1000000000,
"requests":{},"scenarios":[],
"series":[{"second":0,"requests":2,"errors":0,"http_5xx":0,"latency":{"count":2},"concurrency":1}]}`
	rep, err := ReadJSON(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	p := rep.Series[0]
	if p.Requests != 2 || p.NewConns != 0 || p.ReusedConns != 0 || p.DNS.Count != 0 || p.TLS.Count != 0 {
		t.Errorf("p = %+v", p)
	}
}
