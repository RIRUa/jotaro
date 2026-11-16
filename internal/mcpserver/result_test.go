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
			order.Code, order.Err, stepErr = 500, record.ErrExpect, record.ErrExpect
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
		if !out.Passed || out.Error != "" || len(out.Results) != 2 {
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
		if got := out.Results[1].Actual; got != 0.1 { // 2 failures out of 20 requests
			t.Errorf("error_rate actual = %v, want 0.1", got)
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

	for name, args := range map[string]map[string]any{
		"bad threshold": {"path": jsonPath, "thresholds": []string{"p95~200ms"}},
		"missing file":  {"path": filepath.Join(t.TempDir(), "none.json"), "thresholds": []string{"p95<1s"}},
		"no thresholds": {"path": jsonPath, "thresholds": []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			var out CheckResultOutput
			callTool(t, cs, "check_result", args, &out)
			if out.Passed || out.Error == "" {
				t.Errorf("want an error, got %+v", out)
			}
		})
	}
}
