package report_test

import (
	"fmt"
	"os"
	"time"

	"github.com/RIRUa/jotaro/report"
)

// ParseThreshold reads the same syntax that the jotaro check command takes.
func ExampleParseThreshold() {
	for _, s := range []string{"p95<200ms", "p99{step=login}<100ms", "error_rate<1%"} {
		th, err := report.ParseThreshold(s)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println(th.String())
	}
	// Output:
	// p95<200ms
	// p99{step=login}<100ms
	// error_rate<1%
}

// Check judges a report against thresholds, e.g. to fail a CI job.
func ExampleReport_Check() {
	rep := &report.Report{
		Requests: report.RequestStats{
			Count:   100,
			Latency: report.Latency{Count: 100, P95: 120 * time.Millisecond},
		},
	}
	slow, _ := report.ParseThreshold("p95<100ms")
	ok, _ := report.ParseThreshold("p95<200ms")

	checks := rep.Check(ok, slow)
	checks.Print(os.Stdout)
	fmt.Println("passed:", checks.Passed())
	// Output:
	// PASS  p95<200ms                                actual=120ms
	// FAIL  p95<100ms                                actual=120ms
	// NG: 1 of 2 not met
	// passed: false
}
