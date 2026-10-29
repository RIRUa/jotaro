package record

import "testing"

// Every request goes through AddRequest, so it is the hot path of the load generator.
func BenchmarkAddRequest(b *testing.B) {
	c := NewCollector()
	r := Record{Scenario: "checkout", Step: "order", Method: "POST", URL: "/orders", Code: 201}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.AddRequest(r)
	}
}

func BenchmarkAddRequestParallel(b *testing.B) {
	c := NewCollector()
	r := Record{Scenario: "checkout", Step: "order", Method: "POST", URL: "/orders", Code: 201}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.AddRequest(r)
		}
	})
}
