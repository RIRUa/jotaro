package histogram

import (
	"testing"
	"time"
)

// samples returns n durations spread over 1µs to 5ms.
func samples(n int) []time.Duration {
	ds := make([]time.Duration, n)
	for i := range ds {
		ds[i] = time.Duration(i%5000+1) * time.Microsecond
	}
	return ds
}

func BenchmarkNew(b *testing.B) {
	ds := samples(1_000_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New(ds)
	}
}

func BenchmarkPercentile(b *testing.B) {
	h := New(samples(1_000_000))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Percentile(99)
	}
}

func BenchmarkMerge(b *testing.B) {
	x, y := New(samples(1_000_000)), New(samples(500_000))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Merge(x, y)
	}
}
