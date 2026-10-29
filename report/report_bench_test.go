package report

import (
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// benchRecords は 3 ステップ分の n 件のリクエスト記録を返す。
func benchRecords(n int) []record.Record {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	steps := []string{"login", "list", "order"}
	reqs := make([]record.Record, n)
	for i := range reqs {
		start := base.Add(time.Duration(i) * time.Millisecond)
		reqs[i] = record.Record{
			Scenario: "checkout",
			Step:     steps[i%len(steps)],
			UserID:   i % 100,
			Code:     200,
			Conn:     record.ConnReused,
			Start:    start,
			// 時刻をずらして遅延を持たせる
			Scheduled: start.Add(-time.Duration(i%7) * time.Millisecond),
			Latency:   time.Duration(i%5000+1) * time.Microsecond,
			TTFB:      time.Duration(i%2000+1) * time.Microsecond,
		}
	}
	return reqs
}

func BenchmarkBuildReport(b *testing.B) {
	reqs := benchRecords(200_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildReport(reqs, nil, nil, 200*time.Second)
	}
}

func BenchmarkMergeReports(b *testing.B) {
	r1 := BuildReport(benchRecords(100_000), nil, nil, 100*time.Second)
	r2 := BuildReport(benchRecords(100_000), nil, nil, 100*time.Second)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MergeReports(r1, r2); err != nil {
			b.Fatal(err)
		}
	}
}
