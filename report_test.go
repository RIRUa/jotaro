package jotaro

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestNewLatencyPercentiles(t *testing.T) {
	var ds []time.Duration
	for i := 100; i >= 1; i-- { // 逆順でも並べ替えられること
		ds = append(ds, ms(i))
	}
	l := newLatency(ds)

	want := Latency{Count: 100, Min: ms(1), P50: ms(50), P90: ms(90), P95: ms(95), P99: ms(99), Max: ms(100)}
	if l.Count != want.Count || l.Min != want.Min || l.P50 != want.P50 || l.P90 != want.P90 ||
		l.P95 != want.P95 || l.P99 != want.P99 || l.Max != want.Max {
		t.Fatalf("got %+v, want %+v", l, want)
	}
	if l.Mean != 50500*time.Microsecond {
		t.Fatalf("Mean = %v", l.Mean)
	}
	if ds[0] != ms(100) {
		t.Fatal("入力のスライスを並べ替えてはいけない")
	}
}

func TestNewLatencyEdge(t *testing.T) {
	if l := newLatency(nil); l.Count != 0 || l.Max != 0 {
		t.Fatalf("空: %+v", l)
	}
	l := newLatency([]time.Duration{ms(7)})
	if l.P50 != ms(7) || l.P99 != ms(7) || l.Min != ms(7) {
		t.Fatalf("1件: %+v", l)
	}
}

func sampleRecords() ([]Record, []StepRecord, []IterationRecord) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs := []Record{
		{Scenario: "s", Step: "login", Code: 200, Latency: ms(10), TTFB: ms(5), BytesIn: 100, BytesOut: 10, Start: t0},
		{Scenario: "s", Step: "login", Code: 200, Latency: ms(20), TTFB: ms(8), BytesIn: 100, BytesOut: 10, Start: t0},
		{Scenario: "s", Step: "order", Code: 500, Latency: ms(30), TTFB: ms(25), BytesIn: 50, BytesOut: 20, Start: t0},
		{Scenario: "s", Step: "order", Code: 0, Err: ErrTimeout, Latency: ms(1000), Start: t0},
	}
	steps := []StepRecord{
		{Scenario: "s", Step: "login", Start: t0, Duration: ms(10)},
		{Scenario: "s", Step: "login", Start: t0.Add(ms(1)), Duration: ms(20)},
		{Scenario: "s", Step: "order", Start: t0.Add(ms(2)), Duration: ms(30), Err: ErrExpect},
		{Scenario: "s", Step: "order", Start: t0.Add(ms(3)), Duration: ms(1000), Err: ErrTimeout},
	}
	iters := []IterationRecord{
		{Scenario: "s", Start: t0, Duration: ms(500), Think: ms(400)},
		{Scenario: "s", Start: t0, Duration: ms(1200), Think: ms(100), Err: ErrTimeout},
	}
	return reqs, steps, iters
}

func TestBuildReport(t *testing.T) {
	reqs, steps, iters := sampleRecords()
	rep := BuildReport(reqs, steps, iters, 2*time.Second)

	if rep.Requests.Count != 4 || rep.RPS() != 2 {
		t.Fatalf("Count=%d RPS=%v", rep.Requests.Count, rep.RPS())
	}
	if rep.Requests.Status["2xx"] != 2 || rep.Requests.Status["5xx"] != 1 {
		t.Fatalf("Status = %v", rep.Requests.Status)
	}
	if rep.Requests.Errors[ErrTimeout] != 1 {
		t.Fatalf("Errors = %v", rep.Requests.Errors)
	}
	if rep.Requests.BytesIn != 250 || rep.Requests.BytesOut != 40 {
		t.Fatalf("Bytes = %d/%d", rep.Requests.BytesIn, rep.Requests.BytesOut)
	}
	if rep.Requests.TTFB.Count != 3 {
		t.Fatalf("TTFB は応答のあるリクエストだけ: %d", rep.Requests.TTFB.Count)
	}

	if len(rep.Scenarios) != 1 {
		t.Fatalf("Scenarios = %d", len(rep.Scenarios))
	}
	sc := rep.Scenarios[0]
	if sc.Iterations.Count != 2 || sc.Iterations.Failed != 1 || sc.Iterations.Errors[ErrTimeout] != 1 {
		t.Fatalf("Iterations = %+v", sc.Iterations)
	}
	// Active は Think を除く: 100ms と 1100ms
	if sc.Iterations.Active.Min != ms(100) || sc.Iterations.Active.Max != ms(1100) {
		t.Fatalf("Active = %+v", sc.Iterations.Active)
	}
	if sc.Iterations.Total.Max != ms(1200) {
		t.Fatalf("Total = %+v", sc.Iterations.Total)
	}

	if len(sc.Steps) != 2 || sc.Steps[0].Name != "login" || sc.Steps[1].Name != "order" {
		t.Fatalf("Steps の順序: %+v", sc.Steps)
	}
	order := sc.Steps[1]
	if order.Count != 2 || order.Failed != 2 || order.Errors[ErrExpect] != 1 || order.Errors[ErrTimeout] != 1 {
		t.Fatalf("order = %+v", order)
	}
	if order.Requests.Count != 2 || order.Requests.Status["5xx"] != 1 {
		t.Fatalf("order.Requests = %+v", order.Requests)
	}
	if login := sc.Steps[0]; login.Failed != 0 || login.Duration.Max != ms(20) {
		t.Fatalf("login = %+v", login)
	}
}

// ステップが交互に現れても、それぞれの件数が正しく数えられること。
func TestBuildReportInterleavedSteps(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var steps []StepRecord
	for i := 0; i < 4; i++ {
		for j, name := range []string{"a", "b", "c", "d"} {
			steps = append(steps, StepRecord{
				Scenario: "s", Step: name,
				Start:    t0.Add(time.Duration(i*4+j) * time.Millisecond),
				Duration: ms(1),
			})
		}
	}
	rep := BuildReport(nil, steps, nil, time.Second)

	got := rep.Scenarios[0].Steps
	if len(got) != 4 {
		t.Fatalf("Steps = %d, want 4", len(got))
	}
	for i, name := range []string{"a", "b", "c", "d"} {
		if got[i].Name != name || got[i].Count != 4 || got[i].Duration.Count != 4 {
			t.Errorf("Steps[%d] = %+v, want %s x4", i, got[i], name)
		}
	}
}

func TestBuildReportEmpty(t *testing.T) {
	rep := BuildReport(nil, nil, nil, 0)
	if rep.Requests.Count != 0 || len(rep.Scenarios) != 0 || rep.RPS() != 0 {
		t.Fatalf("rep = %+v", rep)
	}
	var buf bytes.Buffer
	rep.Print(&buf) // 空でも落ちない
}

func TestObservedSpan(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs := []Record{
		{Start: t0, Latency: ms(10)},
		{Start: t0.Add(ms(500)), Latency: ms(100)},
	}
	if got := observedSpan(reqs, nil); got != ms(600) {
		t.Fatalf("observedSpan = %v, want 600ms", got)
	}
}

func TestPrint(t *testing.T) {
	reqs, steps, iters := sampleRecords()
	var buf bytes.Buffer
	BuildReport(reqs, steps, iters, 2*time.Second).Print(&buf)
	out := buf.String()

	for _, want := range []string{
		"=== Summary ===",
		"requests:   4 (2.00/s)",
		"2xx=2 5xx=1",
		"timeout=1",
		"=== Scenario: s ===",
		"iterations: 2 (1.00/s), failed 1",
		"active (excl. think)",
		"login", "order",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}

func TestFmtHelpers(t *testing.T) {
	if got := fmtBytes(512); got != "512B" {
		t.Errorf("fmtBytes(512) = %q", got)
	}
	if got := fmtBytes(1536); got != "1.5KB" {
		t.Errorf("fmtBytes(1536) = %q", got)
	}
	if got := fmtBytes(3 << 20); got != "3.0MB" {
		t.Errorf("fmtBytes(3MB) = %q", got)
	}
	if got := fmtDur(0); got != "-" {
		t.Errorf("fmtDur(0) = %q", got)
	}
	if got := fmtDur(1234567 * time.Nanosecond); got != "1.23ms" {
		t.Errorf("fmtDur = %q", got)
	}
	if got := fmtStatus(nil); got != "-" {
		t.Errorf("fmtStatus(nil) = %q", got)
	}
}
