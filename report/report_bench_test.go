package report

import (
	"fmt"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// benchRecords returns n request records spread over 3 steps.
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
			// offset the times to add delay
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

// benchLongReport returns a report of the given number of seconds, with a series point and a health point for every second.
func benchLongReport(seconds int) *Report {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs := make([]record.Record, 0, seconds*5)
	for s := 0; s < seconds; s++ {
		for i := 0; i < 5; i++ {
			reqs = append(reqs, record.Record{
				Scenario: "s", Step: "a", Code: 200, Conn: record.ConnReused,
				Start:   start.Add(time.Duration(s)*time.Second + time.Duration(i)*time.Millisecond),
				Latency: time.Duration(i+1) * time.Millisecond,
			})
		}
	}
	rep := BuildReport(reqs, nil, nil, time.Duration(seconds)*time.Second)
	rep.StartedAt = start
	for s := 0; s < seconds; s++ {
		rep.Health = append(rep.Health, record.HealthPoint{Second: s, Goroutines: 10 + s%7, CPU: 0.3})
	}
	return rep
}

// BenchmarkMergeReportsLong merges two reports of the same length. The time per merge must grow in proportion to the length of the run.
func BenchmarkMergeReportsLong(b *testing.B) {
	for _, seconds := range []int{600, 3600, 21600} {
		r1, r2 := benchLongReport(seconds), benchLongReport(seconds)
		b.Run(fmt.Sprintf("%ds", seconds), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := MergeReports(r1, r2); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
