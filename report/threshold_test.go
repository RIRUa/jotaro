package report

import (
	"bytes"
	"math"
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

// TestParseThresholdQuoted checks quoted selector values: inside the quotes a comma, braces, =, < and > and
// spaces at the ends are literal, and \" and \\ are escapes.
func TestParseThresholdQuoted(t *testing.T) {
	p95 := float64(time.Second)
	tests := []struct {
		in   string
		want Threshold
	}{
		{`p95{step="list, then order"}<1s`, Threshold{Metric: "p95", Step: "list, then order", Op: "<", Value: p95}},
		{`p95{step=" padded "}<1s`, Threshold{Metric: "p95", Step: " padded ", Op: "<", Value: p95}},
		{`p95{step="a}b"}<1s`, Threshold{Metric: "p95", Step: "a}b", Op: "<", Value: p95}},
		{`p95{step="a{b"}<1s`, Threshold{Metric: "p95", Step: "a{b", Op: "<", Value: p95}},
		{`p95{step="a<b>c=d"}<1s`, Threshold{Metric: "p95", Step: "a<b>c=d", Op: "<", Value: p95}},
		{`p95{step="say \"hi\" \\ bye"}<1s`, Threshold{Metric: "p95", Step: `say "hi" \ bye`, Op: "<", Value: p95}},
		{`p95{step="line\nbreak"}<1s`, Threshold{Metric: "p95", Step: "line\nbreak", Op: "<", Value: p95}},
		{`p95{ scenario = "a, b" , step = "c, d" }<1s`, Threshold{Metric: "p95", Scenario: "a, b", Step: "c, d", Op: "<", Value: p95}},
		{`p95{scenario=a,step="c, d"}<1s`, Threshold{Metric: "p95", Scenario: "a", Step: "c, d", Op: "<", Value: p95}},
		{`iteration_p95{scenario="checkout, mobile"}<1s`, Threshold{Metric: "iteration_p95", Scenario: "checkout, mobile", Op: "<", Value: p95}},
		{`p95{step="a"}>=1s`, Threshold{Metric: "p95", Step: "a", Op: ">=", Value: p95}},
		// A quote that does not make the whole value a quoted string is an ordinary character, as before.
		{`p95{step=a"b}<1s`, Threshold{Metric: "p95", Step: `a"b`, Op: "<", Value: p95}},
		{`p95{step="abc}<1s`, Threshold{Metric: "p95", Step: `"abc`, Op: "<", Value: p95}},
		{`p95{step="a"b}<1s`, Threshold{Metric: "p95", Step: `"a"b`, Op: "<", Value: p95}},
		{`p95{step="a" "b"}<1s`, Threshold{Metric: "p95", Step: `"a" "b"`, Op: "<", Value: p95}},
		{`p95{step=GET /users/{id}}<1s`, Threshold{Metric: "p95", Step: "GET /users/{id}", Op: "<", Value: p95}},
		// To select a name that itself starts and ends with quotes, quote it.
		{`p95{step="\"quoted\""}<1s`, Threshold{Metric: "p95", Step: `"quoted"`, Op: "<", Value: p95}},
		// A quoted value is the unquoted name: step="quoted" is quoted, not the name including quotes.
		{`p95{step="quoted"}<1s`, Threshold{Metric: "p95", Step: "quoted", Op: "<", Value: p95}},
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

func TestParseThresholdQuotedErrors(t *testing.T) {
	for _, in := range []string{
		`p95{step=""}<1s`,                      // an empty name selects nothing
		`p95{step="a",}<1s`,                    // trailing comma
		`p95{step="a, b}<1s`,                   // an unterminated quote is not a quoted value: the comma splits it
		`p95{step=list, then order}<1s`,        // an unquoted comma still separates filters
		`p95{step="a"x,scenario}<1s`,           // second filter without a value
		`p95{"step"="a"}<1s`,                   // keys are not quoted
		`p95{step="a\q, b"}<1s`,                // bad escape: not a quoted value, so the comma splits it
		`p95{scenario="a, b"}<1s`,              // scenario alone is still not enough for a request metric
		`rps{step="a, b"}>10`,                  // global metrics cannot be filtered
		`iteration_fail_rate{step="a"}<1`,      // step cannot be used
		`p95{step="a<1s`,                       // never closed
		`p95{step=a}b}<1s`, `p95{step=a{b}<1s`, // unbalanced braces in an unquoted name
	} {
		if _, err := ParseThreshold(in); err == nil {
			t.Errorf("ParseThreshold(%q) should fail", in)
		}
	}
}

// TestParseThresholdNoOperatorHint checks that an operator hidden in an unquoted name is not reported as
// "no operator" without a hint.
func TestParseThresholdNoOperatorHint(t *testing.T) {
	for _, in := range []string{`p95{step=a}b}<1s`, `p95{step=a{b}<1s`} {
		_, err := ParseThreshold(in)
		if err == nil || !strings.Contains(err.Error(), "quote") {
			t.Errorf("ParseThreshold(%q) error = %v, want a hint to quote the name", in, err)
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

// TestThresholdStringRoundTrip checks that String is exact: the values that the display helpers used to round,
// turn into "-" or write in exponent notation come back as the same threshold.
func TestThresholdStringRoundTrip(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"p95>0s", "p95>0s"},
		{"p95<1500500us", "p95<1.5005s"},
		{"p50<1ns", "p50<1ns"},
		{"max<90m", "max<1h30m0s"},
		{"max<2562047h47m16.854775807s", "max<2562047h47m16.854775807s"}, // MaxInt64 ns; its float64 is 2^63, out of int64 range
		{"max>-2562047h47m16.854775808s", "max>-2562047h47m16.854775808s"},
		{"requests>=1234567", "requests>=1234567"},
		{"rps>12345.6", "rps>12345.6"},
		{"dropped<1", "dropped<1"},
		{"rps>0", "rps>0"},
		{"error_rate<1%", "error_rate<1%"},
		{"error_rate<0.07", "error_rate<7%"},
		{"error_rate<0.0001234%", "error_rate<0.0001234%"},
		{"http_5xx_rate<0.123456789", "http_5xx_rate<12.3456789%"},
		{"iteration_fail_rate{scenario=checkout}<=0", "iteration_fail_rate{scenario=checkout}<=0%"},
	} {
		th, err := ParseThreshold(tc.in)
		if err != nil {
			t.Fatalf("ParseThreshold(%q): %v", tc.in, err)
		}
		got := th.String()
		if got != tc.want {
			t.Errorf("ParseThreshold(%q).String() = %q, want %q", tc.in, got, tc.want)
		}
		again, err := ParseThreshold(got)
		if err != nil {
			t.Errorf("%q: String() = %q cannot be parsed: %v", tc.in, got, err)
			continue
		}
		if again != th {
			t.Errorf("%q: changed in round trip: %+v → %+v", tc.in, th, again)
		}
	}
}

// quotedNames are step and scenario names that are awkward in a selector.
var quotedNames = []string{
	"list, then order", "add to cart, 2 items", " padded ", "a}b", "a{b", "{", "}", "}{", "a}{b", "a<b", "a>b", "a<b>c",
	`"quoted"`, `say "hi"`, `\`, `a\"b`, "a=b", "=", "a,b=c", "x\ny", "tab\there", "\x00", "bad\xffutf8", "日本語, 名前",
	"GET /users/{id}", "GET /users/{id}, then {x}", "{a=\"}\"}", `a"`, `"a`, "  ", "\u00a0nbsp",
}

// TestThresholdStringQuotesNames checks that String quotes a step or scenario name when the parser would
// not read it back as it is, leaves ordinary names alone, and that ParseThreshold reads every name back.
func TestThresholdStringQuotesNames(t *testing.T) {
	for _, tc := range []struct {
		th   Threshold
		want string
	}{
		{Threshold{Metric: "p95", Step: "login", Op: "<", Value: 1e9}, "p95{step=login}<1s"},
		{Threshold{Metric: "p95", Step: "GET /users/{id}", Op: "<", Value: 1e9}, "p95{step=GET /users/{id}}<1s"},
		{Threshold{Metric: "p95", Step: "a b", Op: "<", Value: 1e9}, "p95{step=a b}<1s"},
		{Threshold{Metric: "p95", Step: "list, then order", Op: "<", Value: 1e9}, `p95{step="list, then order"}<1s`},
		{Threshold{Metric: "p95", Step: " padded ", Op: "<", Value: 1e9}, `p95{step=" padded "}<1s`},
		{Threshold{Metric: "p95", Step: "a}b", Op: "<", Value: 1e9}, `p95{step="a}b"}<1s`},
		{Threshold{Metric: "p95", Step: `"quoted"`, Op: "<", Value: 1e9}, `p95{step="\"quoted\""}<1s`},
		{Threshold{Metric: "p95", Scenario: "a, b", Step: "c", Op: "<", Value: 1e9}, `p95{scenario="a, b",step=c}<1s`},
		{Threshold{Metric: "iteration_p95", Scenario: "x,y", Op: ">", Value: 1e9}, `iteration_p95{scenario="x,y"}>1s`},
	} {
		got := tc.th.String()
		if got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.th, got, tc.want)
		}
		if back, err := ParseThreshold(got); err != nil || back != tc.th {
			t.Errorf("ParseThreshold(%q) = %+v, %v, want %+v", got, back, err, tc.th)
		}
	}

	for _, name := range quotedNames {
		for _, th := range []Threshold{
			{Metric: "p95", Step: name, Op: "<", Value: 1e9},
			{Metric: "p95", Scenario: name, Step: name, Op: ">=", Value: 1e9},
			{Metric: "p95", Scenario: "s", Step: name, Op: "<", Value: 1e9},
			{Metric: "p95", Scenario: name, Step: "s", Op: "<", Value: 1e9},
			{Metric: "iteration_fail_rate", Scenario: name, Op: "<=", Value: 0.05},
		} {
			got, err := ParseThreshold(th.String())
			if err != nil {
				t.Errorf("%+v: String() = %q cannot be parsed: %v", th, th.String(), err)
				continue
			}
			if got != th {
				t.Errorf("%+v: String() = %q changed in round trip: %+v", th, th.String(), got)
			}
		}
	}
}

// TestThresholdQuotedSelectsStep checks that a quoted name selects the step end to end.
func TestThresholdQuotedSelectsStep(t *testing.T) {
	name := "list, then order"
	rep := &Report{Scenarios: []ScenarioReport{{Name: "main", Steps: []StepStats{{Name: name, Requests: RequestStats{
		Count: 1, Latency: Latency{Count: 1, P95: 50 * time.Millisecond},
	}}}}}}
	th, err := ParseThreshold(`p95{step="list, then order"}<100ms`)
	if err != nil {
		t.Fatal(err)
	}
	checks := rep.Check(th)
	if len(checks) != 1 || !checks[0].Pass || checks[0].Target != "main/"+name {
		t.Fatalf("Check = %+v, want one passing result for main/%s", checks, name)
	}
	var buf bytes.Buffer
	checks.Print(&buf)
	if !strings.Contains(buf.String(), `p95{step="list, then order"}<100ms`) {
		t.Errorf("Print should show a threshold that can be pasted back:\n%s", buf.String())
	}
}

// FuzzThresholdName checks that String writes any scenario and step name in a form ParseThreshold reads back.
func FuzzThresholdName(f *testing.F) {
	for _, n := range quotedNames {
		f.Add(n, n)
	}
	f.Add("login", "checkout")
	f.Fuzz(func(t *testing.T, scenario, step string) {
		th := Threshold{Metric: "p95", Scenario: scenario, Step: step, Op: "<", Value: 1e9}
		if step == "" {
			th = Threshold{Metric: "iteration_p95", Scenario: scenario, Op: "<", Value: 1e9}
		}
		got, err := ParseThreshold(th.String())
		if err != nil {
			t.Fatalf("%+v: String() = %q cannot be parsed: %v", th, th.String(), err)
		}
		if got != th {
			t.Fatalf("%+v: String() = %q changed in round trip: %+v", th, th.String(), got)
		}
	})
}

// FuzzThresholdString checks that every threshold that parses is read back as the same one from its String.
func FuzzThresholdString(f *testing.F) {
	for _, s := range []string{
		"p95<200ms", "p95>0s", "p95<1500500us", "error_rate<1%", "error_rate<0.07", "rps>12345.6",
		"requests>=1234567", "p99{scenario=checkout,step=login}<=1.5s", "iteration_p95{scenario=a}<1h",
		"http_4xx_rate>=1e-9", "rps>1e300", "dropped<0x10", "error_rate<33.333333333333336%",
		`p95{step="list, then order"}<1s`, `p95{step=" padded "}<1s`, `p95{step="a}b"}<1s`, `p95{step="a<b>c"}<1s`,
		`p95{scenario="a, b",step="say \"hi\""}<1s`, `p95{step="quoted"}<1s`, `p95{step="a"b}<1s`, `p95{step="abc}<1s`,
		`iteration_p95{scenario="x\ny"}<1s`, `p95{step=GET /users/{id}}<1s`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		th, err := ParseThreshold(in)
		if err != nil || math.IsNaN(th.Value) {
			t.Skip() // NaN is never equal to itself
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

// TestThresholdFormatValue checks the rounded display used for actual= and the compare table:
// no exponent notation for counts, and "-" for a zero duration.
func TestThresholdFormatValue(t *testing.T) {
	for _, tc := range []struct {
		metric string
		v      float64
		want   string
	}{
		{"requests", 1234567, "1234567"},
		{"rps", 12345.6, "12345.6"},
		{"rps", 1234.5678, "1234.57"},
		{"rps", 0.5, "0.5"},
		{"rps", 0, "0"},
		{"rps", 0.0004, "0.0004"},
		{"error_rate", 1.0 / 300000, "0.000333%"},
		{"error_rate", 0.015, "1.5%"},
		{"error_rate", 0, "0%"},
		{"p95", 0, "-"},
		{"p95", float64(1500500 * time.Microsecond), "1.501s"},
		{"unknown_metric", 1234567, "1234567"},
	} {
		got := Threshold{Metric: tc.metric}.formatValue(tc.v)
		if got != tc.want {
			t.Errorf("formatValue(%s, %v) = %q, want %q", tc.metric, tc.v, got, tc.want)
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
		{Scenario: "checkout", Step: "order", Latency: 200 * ms, Err: record.ErrTimeout}, // no response, like a real transport error
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

func TestParseIterationLatency(t *testing.T) {
	for _, expr := range []string{"iteration_p95<2s", "iteration_active_max{scenario=checkout}<=3s"} {
		if _, err := ParseThreshold(expr); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
	for _, expr := range []string{"iteration_p95{step=a}<1s", "iteration_active_p42<1s", "iteration_<1s", "iteration_p95<5%"} {
		if _, err := ParseThreshold(expr); err == nil {
			t.Errorf("%s: want error", expr)
		}
	}
}

func TestCheckIterationLatency(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	t0 := time.Now()
	iter := func(sc string, i int, dur, think time.Duration) record.IterationRecord {
		return record.IterationRecord{Scenario: sc, UserID: 1, Iteration: i, Start: t0, Duration: dur, Think: think}
	}
	rep := BuildReport(nil, nil, []record.IterationRecord{
		iter("checkout", 0, ms(300), ms(100)),
		iter("checkout", 1, ms(500), ms(100)),
		iter("browse", 0, ms(50), 0),
	}, time.Second)

	tests := []struct {
		expr   string
		pass   bool
		actual time.Duration
	}{
		{"iteration_max{scenario=checkout}<=500ms", true, ms(500)},
		{"iteration_max{scenario=checkout}<500ms", false, ms(500)},
		{"iteration_active_max{scenario=checkout}<=400ms", true, ms(400)}, // Think is left out
		{"iteration_min<=50ms", true, ms(50)},                             // whole run merges the scenarios
		{"iteration_active_min<=50ms", true, ms(50)},
		{"iteration_max<=500ms", true, ms(500)},
	}
	for _, tt := range tests {
		r := checkOne(t, rep, tt.expr)
		if r.Pass != tt.pass || time.Duration(r.Actual) != tt.actual {
			t.Errorf("%s: pass=%v actual=%v, want pass=%v actual=%v (%+v)", tt.expr, r.Pass, time.Duration(r.Actual), tt.pass, tt.actual, r)
		}
	}
	for _, expr := range []string{"iteration_p95{scenario=nothing}<1s"} {
		if r := checkOne(t, rep, expr); r.Pass || r.Note == "" {
			t.Errorf("%s: want failing result with a note: %+v", expr, r)
		}
	}
	empty := BuildReport(nil, nil, nil, time.Second)
	if r := checkOne(t, empty, "iteration_p95<1s"); r.Pass {
		t.Errorf("no iterations must not pass: %+v", r)
	}
}
