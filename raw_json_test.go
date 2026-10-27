package jotaro

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleRaw() *Raw {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms := time.Millisecond
	return &Raw{
		Open: true, Dropped: 2, StartedAt: t0, Elapsed: 3 * time.Second,
		Requests: []Record{
			{Scenario: "checkout", Step: "login", UserID: 1, Iteration: 0, Method: "POST", URL: "http://x/login?a=1&b=2",
				Code: 200, Scheduled: t0.Add(-5 * ms), Start: t0, Latency: 50 * ms, Conn: ConnNew,
				DNS: 5 * ms, Connect: 10 * ms, TLS: 20 * ms, TTFB: 40 * ms, BytesOut: 12, BytesIn: 34},
			{Scenario: "checkout", Step: "order", UserID: 1, Method: "GET", URL: "http://x/orders",
				Err: ErrTimeout, Start: t0.Add(1100 * ms), Latency: 30 * ms, Conn: ConnReused},
		},
		Steps: []StepRecord{
			{Scenario: "checkout", Step: "login", UserID: 1, Start: t0, Duration: 55 * ms},
			{Scenario: "checkout", Step: "order", UserID: 1, Start: t0.Add(1100 * ms), Duration: 30 * ms, Err: ErrTimeout},
		},
		Iterations: []IterationRecord{
			{Scenario: "checkout", UserID: 1, Start: t0, Duration: 1200 * ms, Think: 100 * ms, Err: ErrTimeout},
		},
		Health: []HealthPoint{{Second: 0, Goroutines: 12, CPU: 0.25}, {Second: 1, Goroutines: 9, CPU: -1}},
	}
}

func TestRawRoundTrip(t *testing.T) {
	raw := sampleRaw()
	var buf bytes.Buffer
	if err := WriteRaw(&buf, raw); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRaw(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, raw) {
		t.Errorf("往復で変わった:\n got %+v\nwant %+v", got, raw)
	}
}

func TestRawReportMatchesBuildReport(t *testing.T) {
	raw := sampleRaw()
	var buf bytes.Buffer
	if err := WriteRaw(&buf, raw); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRaw(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if direct, again := raw.Report(), got.Report(); !reflect.DeepEqual(direct, again) {
		t.Errorf("生データから集計し直すと変わった:\n got %+v\nwant %+v", again, direct)
	}
	rep := got.Report()
	if !rep.Open || rep.Dropped != 2 || rep.Elapsed != 3*time.Second || rep.Requests.Count != 2 || len(rep.Health) != 2 {
		t.Errorf("rep = %+v", rep)
	}
}

func TestWriteRawLines(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRaw(&buf, sampleRaw()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 1+2+2+1+2 {
		t.Fatalf("行数 = %d:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], `{"type":"meta","version":1,"open":true,"dropped":2,"started_at":"2026-01-01T00:00:00Z"`) {
		t.Errorf("先頭は meta: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"url":"http://x/login?a=1&b=2"`) {
		t.Errorf("URL の & をエスケープしない: %s", lines[1])
	}
	// 0 や空は省く。
	if strings.Contains(lines[2], "dns_ns") || strings.Contains(lines[2], `"code"`) || strings.Contains(lines[2], "scheduled") {
		t.Errorf("0・空は省くはず: %s", lines[2])
	}
}

func TestReadRawLenient(t *testing.T) {
	// meta が無い、知らない type がある。
	const in = `{"type":"future","x":1}
{"type":"iteration","scenario":"s","start":"2026-01-01T00:00:00Z","duration_ns":1000000}
{"type":"request","scenario":"s","step":"a","start":"2026-01-01T00:00:00Z","latency_ns":2000000,"code":200}
`
	raw, err := ReadRaw(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Requests) != 1 || len(raw.Iterations) != 1 || raw.Elapsed != 0 || raw.Open {
		t.Errorf("raw = %+v", raw)
	}
	if rep := raw.Report(); rep.Elapsed != 2*time.Millisecond || rep.Requests.Status["2xx"] != 1 {
		t.Errorf("実行時間は記録から求めるはず: %+v", rep.Elapsed)
	}
}

func TestReadRawErrors(t *testing.T) {
	cases := map[string]string{
		"壊れた JSON":  `{"type":"meta","version":1}` + "\n" + `{"type":"request",`,
		"新しすぎる版":    `{"type":"meta","version":99}`,
		"版が無い meta": `{"type":"meta"}`,
		"型が違う":      `{"type":"request","latency_ns":"x"}`,
	}
	for name, in := range cases {
		if _, err := ReadRaw(strings.NewReader(in)); err == nil {
			t.Errorf("%s: エラーになるはず", name)
		}
	}
	// どの行で失敗したか分かる。
	_, err := ReadRaw(strings.NewReader(`{"type":"meta","version":1}` + "\n" + `{"type":"request","latency_ns":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "record 2") {
		t.Errorf("err = %v、record 2 を含むはず", err)
	}
}

func TestWriteRawError(t *testing.T) {
	if err := WriteRaw(failWriter{}, sampleRaw()); err == nil {
		t.Error("書き込み失敗がエラーにならない")
	}
}
