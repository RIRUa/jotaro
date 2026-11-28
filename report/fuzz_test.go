package report

import (
	"bytes"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// FuzzParseThreshold feeds arbitrary text to ParseThreshold. It must never panic, and a threshold it accepts
// must survive String → ParseThreshold unchanged.
//
// The value must also be finite and non-negative (and at most 1 for a ratio), because no measurement can be
// NaN, infinite or negative (#82).
func FuzzParseThreshold(f *testing.F) {
	for _, s := range []string{
		"p95<200ms", "error_rate<1%", "rps>12345.6", "requests>=1234567", "dropped<0x10",
		"p99{scenario=checkout,step=login}<=1.5s", `p95{step="list, then order"}<1s`,
		"p95<-1ms", "error_rate<-5%", "rps>NaN", "rps>Inf", "rps>1e999", "p95<1e30h", "error_rate<1e400%",
		"error_rate<500%", "error_rate<1.5", "error_rate<100%", "error_rate<-0%", "rps>-0", "p95<-0s", "rps>-Inf", "rps>1e308",
		"", "<", "p95", "p95<", "{", "p95{", "p95{}<1s", "p95{step=}<1s", "p95{step=a,step=b}<1s",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		th, err := ParseThreshold(in)
		if err != nil {
			return
		}
		if math.IsNaN(th.Value) || math.IsInf(th.Value, 0) || th.Value < 0 || math.Signbit(th.Value) {
			t.Fatalf("%q: parsed to the value %v, which no measurement can meet", in, th.Value)
		}
		if strings.HasSuffix(th.Metric, "_rate") && th.Value > 1 {
			t.Fatalf("%q: parsed to the ratio %v, which is above 100%%", in, th.Value)
		}
		again, err := ParseThreshold(th.String())
		if err != nil {
			t.Fatalf("%q: String() = %q cannot be parsed: %v", in, th.String(), err)
		}
		if again != th {
			t.Fatalf("%q: String() = %q changed in round trip: %+v → %+v", in, th.String(), th, again)
		}
	})
}

// FuzzReadJSON feeds arbitrary bytes to ReadJSON. A damaged or hand-edited result must give an error or a Report
// that every consumer (Write in each format, Check, MergeReports) can handle without panicking or hanging.
func FuzzReadJSON(f *testing.F) {
	for _, s := range fuzzJSONSeeds() {
		f.Add([]byte(s))
	}
	thresholds := []Threshold{
		{Metric: "p95", Op: "<", Value: float64(200 * time.Millisecond)},
		{Metric: "error_rate", Op: "<", Value: 0.01},
		{Metric: "iteration_fail_rate", Scenario: "s", Op: "<=", Value: 0.05},
		{Metric: "p99", Step: "a", Op: "<", Value: float64(time.Second)},
		{Metric: "rps", Op: ">", Value: 1},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rep, err := ReadJSON(bytes.NewReader(data))
		if err != nil {
			return
		}
		for _, format := range []Format{FormatText, FormatJSON, FormatHTML} {
			if err := rep.Write(io.Discard, format); err != nil {
				t.Fatalf("Write(%s) failed: %v", format, err)
			}
		}
		rep.Check(thresholds...).Print(io.Discard)
		if _, err := MergeReports(rep, rep); err != nil {
			t.Fatalf("MergeReports(rep, rep) failed: %v", err)
		}
	})
}

// fuzzJSONSeeds returns the seed inputs of FuzzReadJSON: one valid result written by WriteJSON, and
// small hand-edited variants of the kind the fuzzer should start from.
func fuzzJSONSeeds() []string {
	reqs := []record.Record{
		{Scenario: "s", Step: "a", Code: 200, Latency: 1500 * time.Microsecond, TTFB: 700 * time.Microsecond, BytesIn: 10, BytesOut: 2, Conn: record.ConnNew},
		{Scenario: "s", Step: "a", Err: record.ErrTimeout, Latency: 3 * time.Second},
	}
	steps := []record.StepRecord{{Scenario: "s", Step: "a", Duration: 2 * time.Second}}
	iters := []record.IterationRecord{{Scenario: "s", Duration: 5 * time.Second, Think: time.Second}}
	rep := BuildReport(reqs, steps, iters, 10*time.Second)
	rep.StartedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rep.Series = buildSeries(rep.StartedAt, reqs, iters)
	rep.Health = []record.HealthPoint{{Second: 0, Goroutines: 10, CPU: 0.5}}
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		panic(err)
	}
	valid := buf.String()
	return []string{
		valid,
		`{"version":2}`,
		`{"version":1}`,
		`{"version":99}`,
		`{}`,
		`null`,
		`{"version":2,"requests":{"count":-5,"latency":{"count":-1,"min_ns":-3,"max_ns":9223372036854775807}}}`,
		`{"version":2,"elapsed_ns":-1,"started_at":"0001-01-01T00:00:00Z"}`,
		`{"version":2,"series":[{"second":-1},{"second":2147483647,"requests":1}]}`,
		`{"version":2,"series":[{"second":1000000000000}]}`,
		`{"version":2,"health":[{"second":-7,"goroutines":-1,"cpu":1e308},{"second":99999999999}]}`,
		`{"version":2,"requests":{"count":1,"latency":{"count":1,"buckets":{"5":7,"-3":2,"999999":1}}}}`,
		`{"version":2,"requests":{"count":2,"latency":{"count":2,"min_ns":9223372036854775807,"mean_ns":9223372036854775807,"p50_ns":9223372036854775807,"p95_ns":9223372036854775807,"max_ns":9223372036854775807}}}`,
		`{"version":2,"scenarios":[{"name":"s","steps":[{"name":"a"},{"name":"a"}]},{"name":"s"}]}`,
		`{"version":2} {"version":2}`,
		strings.Repeat(`[`, 100),
	}
}

// TestParseThresholdValueRange pins the inputs that #82 made errors: a parsed value is finite and non-negative, and a ratio is at most 1.
func TestParseThresholdValueRange(t *testing.T) {
	for _, in := range []string{
		"p95<-1ms", "error_rate<-5%", "rps>NaN", "rps>Inf", "rps>1e999", "error_rate<500%", "error_rate<1.5",
		"rps>-Inf", "error_rate<NaN%", "error_rate<-0.0001%", "iteration_p95<-1s", "requests>=-1",
	} {
		if th, err := ParseThreshold(in); err == nil {
			t.Errorf("ParseThreshold(%q) = %+v, want an error", in, th)
		}
	}
}
