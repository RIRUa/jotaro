package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func TestParseThreshold(t *testing.T) {
	tests := []struct {
		in   string
		want Threshold
	}{
		{"p95<200ms", Threshold{Metric: "p95", Op: "<", Value: float64(200 * time.Millisecond)}},
		{" p99 <= 1.5s ", Threshold{Metric: "p99", Op: "<=", Value: float64(1500 * time.Millisecond)}},
		{"error_rate<1%", Threshold{Metric: "error_rate", Op: "<", Value: 0.01}},
		{"error_rate<0.01", Threshold{Metric: "error_rate", Op: "<", Value: 0.01}},
		{"rps>=100", Threshold{Metric: "rps", Op: ">=", Value: 100}},
		{"corrected_p99<1s", Threshold{Metric: "corrected_p99", Op: "<", Value: float64(time.Second)}},
		{"p95{step=login}<100ms", Threshold{Metric: "p95", Step: "login", Op: "<", Value: float64(100 * time.Millisecond)}},
		{"p95{ scenario=checkout , step=login }<100ms", Threshold{Metric: "p95", Scenario: "checkout", Step: "login", Op: "<", Value: float64(100 * time.Millisecond)}},
		{"iteration_fail_rate{scenario=checkout}<=5%", Threshold{Metric: "iteration_fail_rate", Scenario: "checkout", Op: "<=", Value: 0.05}},
	}
	for _, tt := range tests {
		got, err := ParseThreshold(tt.in)
		if err != nil {
			t.Errorf("ParseThreshold(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseThreshold(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseThresholdErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"p95",                           // no operator
		"p95<",                          // no value
		"p95<200",                       // no unit
		"p42<200ms",                     // unknown metric
		"error_rate<abc",                // not readable as a ratio
		"p95{step=login<100ms",          // missing }
		"p95{step}<100ms",               // not key=value
		"p95{user=1}<100ms",             // unknown key
		"p95{scenario=checkout}<100ms",  // cannot select by scenario without step
		"rps{step=login}>10",            // can only be evaluated for the whole run
		"iteration_fail_rate{step=a}<1", // step cannot be used
		"p95=200ms",                     // = is not an operator
	} {
		if _, err := ParseThreshold(in); err == nil {
			t.Errorf("ParseThreshold(%q) should fail", in)
		}
	}
}

func TestThresholdString(t *testing.T) {
	for _, in := range []string{
		"p95<200ms",
		"error_rate<1%",
		"p99{scenario=checkout,step=login}<=1.5s",
		"rps>=100",
	} {
		th, err := ParseThreshold(in)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseThreshold(th.String())
		if err != nil {
			t.Fatalf("cannot parse String() = %q: %v", th.String(), err)
		}
		if again != th {
			t.Errorf("%q: changed in round trip: %+v → %+v", in, th, again)
		}
	}
}

// checkReport is a small result with 2 scenarios and 3 steps.
func checkReport() *Report {
	ms := time.Millisecond
	reqs := []record.Record{
		{Scenario: "checkout", Step: "login", Code: 200, Latency: 10 * ms},
		{Scenario: "checkout", Step: "login", Code: 200, Latency: 20 * ms},
		{Scenario: "checkout", Step: "order", Code: 500, Latency: 100 * ms},
		{Scenario: "checkout", Step: "order", Code: 201, Latency: 200 * ms, Err: record.ErrExpect},
		{Scenario: "browse", Step: "login", Code: 404, Latency: 30 * ms},
	}
	iters := []record.IterationRecord{
		{Scenario: "checkout", Duration: 300 * ms},
		{Scenario: "checkout", Duration: 300 * ms, Err: record.ErrExpect},
		{Scenario: "browse", Duration: 30 * ms},
	}
	steps := []record.StepRecord{
		{Scenario: "checkout", Step: "login", Duration: 20 * ms},
		{Scenario: "checkout", Step: "order", Duration: 200 * ms},
		{Scenario: "browse", Step: "login", Duration: 30 * ms},
	}
	rep := BuildReport(reqs, steps, iters, time.Second)
	rep.Dropped = 3
	return rep
}

func checkOne(t *testing.T, rep *Report, expr string) CheckResult {
	t.Helper()
	th, err := ParseThreshold(expr)
	if err != nil {
		t.Fatal(err)
	}
	cs := rep.Check(th)
	if len(cs) != 1 {
		t.Fatalf("%s: %d results: %+v", expr, len(cs), cs)
	}
	return cs[0]
}

func TestCheck(t *testing.T) {
	rep := checkReport()
	tests := []struct {
		expr string
		pass bool
	}{
		{"max<=200ms", true},
		{"max<200ms", false},
		{"p50<50ms", true},
		{"error_rate<=20%", true}, // 1/5
		{"error_rate<20%", false},
		{"http_5xx_rate<=20%", true},
		{"http_4xx_rate<20%", false}, // 1/5 = 20%
		{"requests>=5", true},
		{"rps>=5", true},
		{"rps>5", false},
		{"dropped<3", false},
		{"dropped<=3", true},
	}
	for _, tt := range tests {
		if r := checkOne(t, rep, tt.expr); r.Pass != tt.pass {
			t.Errorf("%s: pass = %v, want %v (%+v)", tt.expr, r.Pass, tt.pass, r)
		}
	}
}

func TestCheckIterationFailRate(t *testing.T) {
	rep := checkReport()
	if r := checkOne(t, rep, "iteration_fail_rate<=34%"); !r.Pass {
		t.Errorf("whole run should be 1/3: %+v", r)
	}
	if r := checkOne(t, rep, "iteration_fail_rate<30%"); r.Pass {
		t.Errorf("whole run should be 1/3: %+v", r)
	}
	if r := checkOne(t, rep, "iteration_fail_rate{scenario=checkout}<=50%"); !r.Pass || r.Actual != 0.5 {
		t.Errorf("checkout should be 1/2: %+v", r)
	}
	if r := checkOne(t, rep, "iteration_fail_rate{scenario=browse}<1%"); !r.Pass {
		t.Errorf("browse should be 0: %+v", r)
	}
}

func TestCheckStepScope(t *testing.T) {
	rep := checkReport()

	// Specifying the scenario too gives one result.
	r := checkOne(t, rep, "max{scenario=checkout,step=order}<=200ms")
	if !r.Pass || r.Target != "checkout/order" {
		t.Errorf("%+v", r)
	}
	if r := checkOne(t, rep, "error_rate{scenario=checkout,step=order}<50%"); r.Pass {
		t.Errorf("network errors of order should be 1/2: %+v", r)
	}

	// login is in two scenarios, so check them separately.
	th, _ := ParseThreshold("max{step=login}<=20ms")
	cs := rep.Check(th)
	if len(cs) != 2 {
		t.Fatalf("%d results: %+v", len(cs), cs)
	}
	if cs.Passed() {
		t.Error("browse/login is 30ms, so it should not be met")
	}
	got := map[string]bool{}
	for _, c := range cs {
		got[c.Target] = c.Pass
	}
	if !got["checkout/login"] || got["browse/login"] {
		t.Errorf("%v", got)
	}
}

func TestCheckFailsWithoutTarget(t *testing.T) {
	rep := checkReport()
	for _, expr := range []string{
		"p95{step=nothing}<1s",
		"iteration_fail_rate{scenario=nothing}<50%",
		"ttfb_p95<1s", // no TTFB records
		"dns_p95<1s",  // the phase did not happen
	} {
		r := checkOne(t, rep, expr)
		if r.Pass || r.Note == "" {
			t.Errorf("%s: with no target or data, it should count as not met: %+v", expr, r)
		}
	}

	// A result with 0 requests does not meet a ratio either.
	empty := BuildReport(nil, nil, nil, time.Second)
	if r := checkOne(t, empty, "error_rate<1%"); r.Pass {
		t.Errorf("must not succeed with 0 requests: %+v", r)
	}
}

func TestChecksPrint(t *testing.T) {
	rep := checkReport()
	var ts []Threshold
	for _, e := range []string{"max<=200ms", "p50<10ms", "p95{step=nothing}<1s"} {
		th, err := ParseThreshold(e)
		if err != nil {
			t.Fatal(err)
		}
		ts = append(ts, th)
	}
	cs := rep.Check(ts...)
	if cs.Passed() {
		t.Fatal("contains one that is not met")
	}
	var buf bytes.Buffer
	cs.Print(&buf)
	out := buf.String()
	for _, want := range []string{"PASS  max<=200ms", "FAIL  p50<10ms", "no matching step", "NG: 2 of 3 not met"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from output:\n%s", want, out)
		}
	}
}
