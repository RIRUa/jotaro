package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/internal/memnet"
	"github.com/RIRUa/jotaro/report"
)

// start runs the real plan, only shorter, against mock. tweak, if not nil, may change the plan first.
func start(t *testing.T, d time.Duration, mock *shop, tweak func(*jotaro.Plan)) (*report.Report, error) {
	t.Helper()
	// An in-memory network inside a synctest bubble (the callers): the mock's latency and the think times use
	// the fake clock, so a run of seconds takes milliseconds.
	srv := memnet.Start(mock.handler())
	t.Cleanup(srv.Close)
	plan := newPlan(srv.URL, d)
	if tweak != nil {
		tweak(&plan)
	}
	return jotaro.Run(context.Background(), plan)
}

// Both scenarios run against a healthy shop, nothing fails, and every threshold finds something to judge.
// The scenarios are picked at random, so the test only asks that each scenario ran at all, and whether the
// latency goals are met is not tested either.
func TestMixRuns(t *testing.T) { synctest.Test(t, testMixRuns) }

func testMixRuns(t *testing.T) {
	rep, err := start(t, 3*time.Second, &shop{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, sc := range rep.Scenarios {
		seen[sc.Name] = sc.Iterations.Count
		if sc.Iterations.Failed != 0 {
			t.Errorf("%s: %d of %d iterations failed: %v", sc.Name, sc.Iterations.Failed, sc.Iterations.Count, sc.Iterations.Errors)
		}
	}
	if seen["browse"] == 0 || seen["buy"] == 0 {
		t.Errorf("iterations per scenario = %v, want both scenarios to run", seen)
	}
	if len(rep.Requests.Errors) != 0 {
		t.Errorf("transport errors: %v", rep.Requests.Errors)
	}

	for _, s := range thresholds {
		th, err := report.ParseThreshold(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		for _, r := range rep.Check(th) {
			if r.Note != "" {
				t.Errorf("%q: %s", s, r.Note)
			}
		}
	}
}

// A shop that answers 503 to everything makes AbortOn stop the run long before Duration. Run then returns the
// report up to that point together with an error that wraps ErrAborted.
func TestMixAborts(t *testing.T) { synctest.Test(t, testMixAborts) }

func testMixAborts(t *testing.T) {
	mock := &shop{}
	mock.broken.Store(true)

	rep, err := start(t, 30*time.Second, mock, func(p *jotaro.Plan) {
		for i := range p.AbortOn {
			p.AbortOn[i].For = 0 // abort at the first evaluation (every second) where the condition holds
		}
	})
	if !errors.Is(err, jotaro.ErrAborted) {
		t.Fatalf("err = %v, want one that wraps ErrAborted", err)
	}
	if rep == nil {
		t.Fatal("no report returned with the abort")
	}
	if rep.Requests.Count == 0 || rep.Requests.Status["5xx"] == 0 {
		t.Errorf("the partial report has no 5xx responses: %+v", rep.Requests.Status)
	}
	// The condition holds at the first evaluation, after 1s, and the users that are running then finish within
	// a few seconds (the shop is down, so the requests are answered at once).
	if want := "(at 1s)"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want the abort at the first evaluation %s", err, want)
	}
	if rep.Elapsed < time.Second || rep.Elapsed > 6*time.Second {
		t.Errorf("the run took %v, so it was not stopped early (30s planned)", rep.Elapsed)
	}
}
