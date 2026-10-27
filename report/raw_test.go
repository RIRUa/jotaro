package report

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func rawFixture() *record.Raw {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms := time.Millisecond
	return &record.Raw{
		Open: true, Dropped: 2, StartedAt: t0, Elapsed: 3 * time.Second,
		Requests: []record.Record{
			{Scenario: "checkout", Step: "login", UserID: 1, Iteration: 0, Method: "POST", URL: "http://x/login?a=1&b=2",
				Code: 200, Scheduled: t0.Add(-5 * ms), Start: t0, Latency: 50 * ms, Conn: record.ConnNew,
				DNS: 5 * ms, Connect: 10 * ms, TLS: 20 * ms, TTFB: 40 * ms, BytesOut: 12, BytesIn: 34},
			{Scenario: "checkout", Step: "order", UserID: 1, Method: "GET", URL: "http://x/orders",
				Err: record.ErrTimeout, Start: t0.Add(1100 * ms), Latency: 30 * ms, Conn: record.ConnReused},
		},
		Steps: []record.StepRecord{
			{Scenario: "checkout", Step: "login", UserID: 1, Start: t0, Duration: 55 * ms},
			{Scenario: "checkout", Step: "order", UserID: 1, Start: t0.Add(1100 * ms), Duration: 30 * ms, Err: record.ErrTimeout},
		},
		Iterations: []record.IterationRecord{
			{Scenario: "checkout", UserID: 1, Start: t0, Duration: 1200 * ms, Think: 100 * ms, Err: record.ErrTimeout},
		},
		Health: []record.HealthPoint{{Second: 0, Goroutines: 12, CPU: 0.25}, {Second: 1, Goroutines: 9, CPU: -1}},
	}
}

func TestFromRaw(t *testing.T) {
	raw := rawFixture()
	var buf bytes.Buffer
	if err := record.WriteRaw(&buf, raw); err != nil {
		t.Fatal(err)
	}
	got, err := record.ReadRaw(&buf)
	if err != nil {
		t.Fatal(err)
	}
	// 書き出して読み戻した生データから集計しても、元の生データから集計した結果と変わらない。
	if direct, again := FromRaw(raw), FromRaw(got); !reflect.DeepEqual(direct, again) {
		t.Errorf("生データから集計し直すと変わった:\n got %+v\nwant %+v", again, direct)
	}
	rep := FromRaw(got)
	if !rep.Open || rep.Dropped != 2 || rep.Elapsed != 3*time.Second || rep.Requests.Count != 2 || len(rep.Health) != 2 {
		t.Errorf("rep = %+v", rep)
	}
}

// meta が無い生データは、実行時間を記録から求める。
func TestFromRawWithoutMeta(t *testing.T) {
	const in = `{"type":"iteration","scenario":"s","start":"2026-01-01T00:00:00Z","duration_ns":1000000}
{"type":"request","scenario":"s","step":"a","start":"2026-01-01T00:00:00Z","latency_ns":2000000,"code":200}
`
	raw, err := record.ReadRaw(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if rep := FromRaw(raw); rep.Elapsed != 2*time.Millisecond || rep.Requests.Status["2xx"] != 1 {
		t.Errorf("実行時間は記録から求めるはず: %+v", rep.Elapsed)
	}
}
