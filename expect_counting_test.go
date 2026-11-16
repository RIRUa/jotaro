package jotaro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// checkExpr evaluates one threshold expression against rep and returns its single result.
func checkExpr(t *testing.T, rep *report.Report, expr string) report.CheckResult {
	t.Helper()
	th, err := report.ParseThreshold(expr)
	if err != nil {
		t.Fatal(err)
	}
	cs := rep.Check(th)
	if len(cs) != 1 {
		t.Fatalf("%s: %d results: %+v", expr, len(cs), cs)
	}
	return cs[0]
}

// TestExpectMismatchCounting pins how a failed Expect* check shows up from Run to Report.Check:
// error_rate counts transport-level errors only, so a run in which every response mismatches still has
// error_rate 0. The mismatch is recorded as expect_mismatch on the step, and on the iteration when the
// scenario returns the error.
func TestExpectMismatchCounting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	run := func(t *testing.T, returnErr bool) *report.Report {
		t.Helper()
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			err := u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
			if returnErr {
				return err
			}
			time.Sleep(time.Millisecond) // keep the loop from spinning when errors are swallowed
			return nil
		})
		rep, err := Run(context.Background(), Plan{Scenario: sc, BaseURL: srv.URL, Users: 1, Duration: 200 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Requests.Count == 0 {
			t.Fatal("no requests were made")
		}
		return rep
	}

	t.Run("mismatch is a step error, not a request error", func(t *testing.T) {
		rep := run(t, false)
		if len(rep.Requests.Errors) != 0 {
			t.Errorf("request errors = %v, want none", rep.Requests.Errors)
		}
		if got := rep.Requests.Status["5xx"]; got != rep.Requests.Count {
			t.Errorf("5xx = %d, want all %d requests", got, rep.Requests.Count)
		}
		step := rep.Scenarios[0].Steps[0]
		if step.Count != rep.Requests.Count || step.Failed != step.Count || step.Errors[record.ErrExpect] != step.Count {
			t.Errorf("step = %+v, want every run failed with %s", step, record.ErrExpect)
		}
		if len(step.Requests.Errors) != 0 {
			t.Errorf("step request errors = %v, want none", step.Requests.Errors)
		}

		// The scenario swallowed the error, so the iterations did not fail either.
		if it := rep.Scenarios[0].Iterations; it.Failed != 0 {
			t.Errorf("iterations failed = %d, want 0", it.Failed)
		}
		if r := checkExpr(t, rep, "error_rate<1%"); !r.Pass || r.Actual != 0 {
			t.Errorf("error_rate = %+v, want 0 and passing", r)
		}
		if r := checkExpr(t, rep, "iteration_fail_rate<1%"); !r.Pass {
			t.Errorf("iteration_fail_rate = %+v, want passing", r)
		}
		if r := checkExpr(t, rep, "http_5xx_rate<1%"); r.Pass || r.Actual != 1 {
			t.Errorf("http_5xx_rate = %+v, want 1 and failing", r)
		}
	})

	t.Run("returning the mismatch reaches iteration_fail_rate", func(t *testing.T) {
		rep := run(t, true)
		it := rep.Scenarios[0].Iterations
		if it.Count == 0 || it.Failed != it.Count || it.Errors[record.ErrExpect] != it.Count {
			t.Errorf("iterations = %+v, want all failed with %s", it, record.ErrExpect)
		}
		if r := checkExpr(t, rep, "iteration_fail_rate<1%"); r.Pass || r.Actual != 1 {
			t.Errorf("iteration_fail_rate = %+v, want 1 and failing", r)
		}
		if r := checkExpr(t, rep, "error_rate<1%"); !r.Pass || r.Actual != 0 {
			t.Errorf("error_rate = %+v, want 0 and passing", r)
		}
	})
}
