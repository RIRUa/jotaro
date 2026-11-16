package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// sampleRaw is a small open-model run: scenario "checkout" with steps "login" (fast, all 200)
// and "order" (slow, one 500 that failed its expectation and one timeout), plus two dropped arrivals.
// As in a real run, the failed expectation is in the step and iteration records only: the request record
// of that 500 has no Err, because only transport-level errors are recorded on requests.
func sampleRaw() *record.Raw {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	raw := &record.Raw{StartedAt: start, Elapsed: 10 * time.Second, Open: true, Dropped: 2}
	for i := range 10 {
		at := start.Add(time.Duration(i) * time.Second)
		raw.Requests = append(raw.Requests, record.Record{
			Scenario: "checkout", Step: "login", Iteration: i, Method: "POST", URL: "/login",
			Code: 200, Scheduled: at, Start: at, Latency: 10 * time.Millisecond, TTFB: 5 * time.Millisecond,
		})
		order := record.Record{
			Scenario: "checkout", Step: "order", Iteration: i, Method: "POST", URL: "/orders",
			Code: 201, Scheduled: at, Start: at, Latency: 300 * time.Millisecond, TTFB: 200 * time.Millisecond,
		}
		stepErr := record.ErrNone
		switch i {
		case 0:
			order.Code, stepErr = 500, record.ErrExpect
		case 1:
			order.Code, order.Err, stepErr = 0, record.ErrTimeout, record.ErrTimeout
		}
		raw.Requests = append(raw.Requests, order)
		raw.Steps = append(raw.Steps,
			record.StepRecord{Scenario: "checkout", Step: "login", Iteration: i, Start: at, Duration: 10 * time.Millisecond},
			record.StepRecord{Scenario: "checkout", Step: "order", Iteration: i, Start: at, Duration: 300 * time.Millisecond, Err: stepErr},
		)
		raw.Iterations = append(raw.Iterations, record.IterationRecord{
			Scenario: "checkout", Iteration: i, Start: at, Duration: 310 * time.Millisecond, Err: stepErr,
		})
	}
	return raw
}

// writeResult saves the sample as a JSON result and as raw data, and returns both paths.
func writeResult(t *testing.T, raw *record.Raw) (jsonPath, rawPath string) {
	t.Helper()
	dir := t.TempDir()

	jsonPath = filepath.Join(dir, "result.json")
	f, err := os.Create(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.FromRaw(raw).WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rawPath = filepath.Join(dir, "raw.jsonl")
	f, err = os.Create(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.WriteRaw(f, raw); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return jsonPath, rawPath
}

func TestCheckResult(t *testing.T) {
	cs := connect(t)
	jsonPath, rawPath := writeResult(t, sampleRaw())

	t.Run("met", func(t *testing.T) {
		var out CheckResultOutput
		callTool(t, cs, "check_result", map[string]any{
			"path":       jsonPath,
			"thresholds": []string{"p95{step=login}<100ms", "requests>=20"},
		}, &out)
		if !out.Passed || len(out.Results) != 2 {
			t.Fatalf("want both met, got %+v", out)
		}
		r := out.Results[0]
		if r.Target != "checkout/login" || !r.Pass || r.Actual != float64(10*time.Millisecond) {
			t.Errorf("unexpected result %+v", r)
		}
		if !strings.Contains(out.Text, "OK: all 2 met") {
			t.Errorf("text does not report the outcome:\n%s", out.Text)
		}
	})

	t.Run("not met", func(t *testing.T) {
		var out CheckResultOutput
		callTool(t, cs, "check_result", map[string]any{
			"path":       jsonPath,
			"thresholds": []string{"p95{step=order}<100ms", "error_rate<1%"},
		}, &out)
		if out.Passed || out.Results[0].Pass || out.Results[1].Pass {
			t.Fatalf("want both not met, got %+v", out)
		}
		if got := out.Results[1].Actual; got != 0.05 { // 1 transport error out of 20 requests; the failed expectation is not counted
			t.Errorf("error_rate actual = %v, want 0.05", got)
		}
	})

	t.Run("unknown step is not met", func(t *testing.T) {
		var out CheckResultOutput
		callTool(t, cs, "check_result", map[string]any{
			"path":       jsonPath,
			"thresholds": []string{"p95{step=logni}<100ms"},
		}, &out)
		if out.Passed || out.Results[0].Note == "" {
			t.Errorf("want a note about the missing step, got %+v", out)
		}
	})

	t.Run("raw data", func(t *testing.T) {
		var out CheckResultOutput
		callTool(t, cs, "check_result", map[string]any{
			"path": rawPath, "raw": true, "thresholds": []string{"dropped<=2"},
		}, &out)
		if !out.Passed {
			t.Errorf("want met from raw data, got %+v", out)
		}
	})

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string // a part of the error text
	}{
		"bad threshold": {map[string]any{"path": jsonPath, "thresholds": []string{"p95~200ms"}}, "threshold"},
		"missing file":  {map[string]any{"path": filepath.Join(dir, "none.json"), "thresholds": []string{"p95<1s"}}, "none.json"},
		"no thresholds": {map[string]any{"path": jsonPath, "thresholds": []string{}}, "no thresholds"},
		"empty path":    {map[string]any{"path": "", "thresholds": []string{"p95<1s"}}, "path is empty"},
		// An aggregated JSON result with raw: true must not read as an empty run that fails with "no data".
		"aggregated JSON as raw": {map[string]any{"path": jsonPath, "raw": true, "thresholds": []string{"p95<1s"}}, "raw: true"},
		"empty file as raw":      {map[string]any{"path": empty, "raw": true, "thresholds": []string{"p95<1s"}}, "raw: true"},
		"raw data without raw":   {map[string]any{"path": rawPath, "thresholds": []string{"p95<1s"}}, "raw: true"},
	} {
		t.Run(name, func(t *testing.T) {
			msg := callToolError(t, cs, "check_result", tc.args)
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.want)
			}
			for _, bad := range []string{"-raw", "ReadRaw", "jotaro:"} {
				if strings.Contains(msg, bad) {
					t.Errorf("error = %q should not mention %q", msg, bad)
				}
			}
		})
	}
}

func TestSummarizeResult(t *testing.T) {
	cs := connect(t)
	raw := sampleRaw()
	raw.Health = []record.HealthPoint{{Second: 0, Goroutines: 40, CPU: 0.5}, {Second: 1, Goroutines: 60, CPU: 0.95}}
	jsonPath, rawPath := writeResult(t, raw)

	for name, args := range map[string]map[string]any{
		"json": {"path": jsonPath},
		"raw":  {"path": rawPath, "raw": true},
	} {
		t.Run(name, func(t *testing.T) {
			var out SummaryOutput
			callTool(t, cs, "summarize_result", args, &out)
			if out.Model != "open" || out.ElapsedSeconds != 10 || out.RPS != 2 || out.Dropped != 2 || out.PeakConcurrency != 1 {
				t.Errorf("unexpected totals %+v", out)
			}
			if q := out.Requests; q.Count != 20 || q.ErrorRate != 0.05 || q.Corrected == nil ||
				q.Errors["timeout"] != 1 || len(q.Errors) != 1 || q.Status["5xx"] != 1 {
				t.Errorf("unexpected request summary %+v", q)
			}
			if len(out.Scenarios) != 1 || len(out.Scenarios[0].Steps) != 2 {
				t.Fatalf("want one scenario with two steps, got %+v", out.Scenarios)
			}
			sc := out.Scenarios[0]
			if sc.Name != "checkout" || sc.Iterations != 10 || sc.FailedIterations != 2 {
				t.Errorf("unexpected scenario %+v", sc)
			}
			login, order := sc.Steps[0], sc.Steps[1]
			if login.Name != "login" || login.Failed != 0 || login.Requests.Latency.P95Ms != 10 {
				t.Errorf("unexpected login step %+v", login)
			}
			if order.Name != "order" || order.Failed != 2 || order.Requests.Latency.P95Ms != 300 || order.Requests.ErrorRate != 0.1 {
				t.Errorf("unexpected order step %+v", order)
			}
			if out.WorstSecond == nil || out.WorstSecond.P95Ms != 300 || out.WorstSecond.Requests != 2 {
				t.Errorf("unexpected worst second %+v", out.WorstSecond)
			}
			if lg := out.LoadGenerator; lg == nil || lg.MaxCPU == nil || *lg.MaxCPU != 0.95 || lg.MaxGoroutines != 60 {
				t.Errorf("unexpected load generator summary %+v", lg)
			}
			if len(out.Warnings) != 2 || !strings.Contains(out.Warnings[0], "dropped") || !strings.Contains(out.Warnings[1], "CPU") {
				t.Errorf("want warnings about dropped arrivals and CPU, got %q", out.Warnings)
			}
		})
	}

	t.Run("closed model without warnings", func(t *testing.T) {
		raw := sampleRaw()
		raw.Open, raw.Dropped = false, 0
		jsonPath, _ := writeResult(t, raw)
		var out SummaryOutput
		callTool(t, cs, "summarize_result", map[string]any{"path": jsonPath}, &out)
		if out.Model != "closed" || out.Requests.Corrected != nil || out.LoadGenerator != nil || len(out.Warnings) != 0 {
			t.Errorf("unexpected summary %+v", out)
		}
	})

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"missing file":           {map[string]any{"path": filepath.Join(dir, "none.json")}, "none.json"},
		"aggregated JSON as raw": {map[string]any{"path": jsonPath, "raw": true}, "raw: true"},
		"empty file as raw":      {map[string]any{"path": empty, "raw": true}, "raw: true"},
		"raw data without raw":   {map[string]any{"path": rawPath}, "raw: true"},
		"not a result":           {map[string]any{"path": empty}, "EOF"},
	} {
		t.Run(name, func(t *testing.T) {
			// A file that cannot be read is a tool error, not a summary of zeros.
			msg := callToolError(t, cs, "summarize_result", tc.args)
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.want)
			}
		})
	}
}

// TestRawWithoutRequests pins that a valid raw stream of a run without requests (a meta line only) is not
// mistaken for the wrong kind of input.
func TestRawWithoutRequests(t *testing.T) {
	cs := connect(t)
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"meta","version":1,"elapsed_ns":1000000000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out SummaryOutput
	callTool(t, cs, "summarize_result", map[string]any{"path": path, "raw": true}, &out)
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "no requests") {
		t.Errorf("want the no-requests warning, got %+v", out)
	}
}
