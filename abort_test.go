package jotaro

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// failingServer answers 500 to everything after 7ms. The delay is not a detail: a stub that answered at once
// would let a closed-model user spin without ever blocking, and the fake clock of a bubble moves only when
// every goroutine is blocked. 7ms never ties with a whole second of the abort evaluation or the progress line.
func failingServer() *fakeServer {
	return fakeServerStatus(7*time.Millisecond, http.StatusInternalServerError)
}

func getScenario() *Scenario {
	return NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
}

func TestAbortRuleValidation(t *testing.T) {
	base := Plan{Scenario: getScenario(), Users: 1, Duration: time.Second}
	for name, rules := range map[string][]AbortRule{
		"no operator":   {{When: "error_rate"}},
		"unknown":       {{When: "nope>1"}},
		"empty":         {{}},
		"negative For":  {{When: "http_5xx_rate>50%", For: -time.Second}},
		"bad value":     {{When: "p95>fast"}},
		"second broken": {{When: "http_5xx_rate>50%"}, {When: "p95"}},
	} {
		p := base
		p.AbortOn = rules
		if _, err := Run(context.Background(), p); err == nil || errors.Is(err, ErrAborted) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	p := base
	p.AbortOn = []AbortRule{{When: "http_5xx_rate>50%", For: 5 * time.Second}, {When: "p99{step=get}>2s"}}
	if err := p.validate(); err != nil {
		t.Errorf("valid rules rejected: %v", err)
	}
}

func TestAbortStateHoldsFor(t *testing.T) {
	rules, err := parseAbortRules([]AbortRule{
		{When: "http_5xx_rate>50%", For: 3 * time.Second},
		{When: "p95>1s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := newAbortState(rules)
	t0 := time.Now()
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

	steps := []struct {
		sec  int
		held []bool
		want int // index of the rule that fires, -1 for none
	}{
		{1, []bool{true, false}, -1},
		{2, []bool{true, false}, -1},
		{3, []bool{false, false}, -1}, // a break resets the first rule
		{4, []bool{true, false}, -1},
		{5, []bool{true, false}, -1},
		{6, []bool{true, false}, -1}, // held for 2s only
		{7, []bool{true, false}, 0},  // held for 3s
	}
	for _, s := range steps {
		r, ok := st.step(at(s.sec), s.held)
		got := -1
		if ok {
			got = 0
			if r.rule.When != rules[0].rule.When {
				got = 1
			}
		}
		if got != s.want {
			t.Fatalf("t=%ds: fired rule %d, want %d", s.sec, got, s.want)
		}
	}

	// For 0 fires at the first evaluation where the condition holds.
	st = newAbortState(rules)
	if r, ok := st.step(at(1), []bool{false, true}); !ok || r.rule.When != "p95>1s" {
		t.Errorf("For=0 rule did not fire: %v %v", r, ok)
	}
}

// A run with AbortOn {5xx rate > 50%, For: 5s} against a server that fails every request stops at the 6th
// evaluation: the condition holds from the first one (1s) and has held for 5s at 6s. The Duration is a minute and
// the Grace the default 30s, none of which is waited for.
func TestRunAbortsEarly(t *testing.T) {
	for name, plan := range map[string]Plan{
		"closed": {Users: 2},
		"staged": {UserStages: []UserStage{{Target: 2, For: time.Second}}},
		"open":   {Arrival: Rate(200, time.Second)},
	} {
		t.Run(name, func(t *testing.T) {
			inBubble(t, func(t *testing.T) {
				var progress syncBuffer
				plan.Scenario, plan.BaseURL, plan.Duration = getScenario(), "http://stub.test", time.Minute
				plan.AbortOn = []AbortRule{{When: "http_5xx_rate>50%", For: 5 * time.Second}}
				plan.Progress = &progress

				rep, err := run(context.Background(), plan, failingServer())
				if !errors.Is(err, ErrAborted) {
					t.Fatalf("err = %v, want ErrAborted", err)
				}
				if want := "jotaro: run aborted: http_5xx_rate>50% for 5s (at 6s)"; err.Error() != want {
					t.Errorf("err = %q, want %q", err, want)
				}
				if rep == nil || rep.Requests.Count == 0 {
					t.Fatalf("partial report missing: %+v", rep)
				}
				// The abort comes at 6s; the requests in flight then (7ms each) finish before 6.007s.
				if rep.Elapsed < 6*time.Second || rep.Elapsed > 6*time.Second+7*time.Millisecond {
					t.Errorf("Elapsed = %v, want 6s plus the request that was in flight", rep.Elapsed)
				}
				if !strings.Contains(progress.String(), "aborted: http_5xx_rate>50% for 5s (at 6s)\n") {
					t.Errorf("no abort line in progress output: %q", progress.String())
				}
			})
		})
	}
}

// The raw data of an aborted run is still written.
func TestRunAbortWithRaw(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		var raw bytes.Buffer
		rep, err := run(context.Background(), Plan{
			Scenario: getScenario(), BaseURL: "http://stub.test", Users: 2, Duration: 30 * time.Second,
			AbortOn: []AbortRule{{When: "http_5xx_rate>50%"}}, Raw: &raw,
		}, failingServer())
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
		if raw.Len() == 0 || rep.Requests.Count == 0 {
			t.Errorf("raw = %d bytes, requests = %d", raw.Len(), rep.Requests.Count)
		}
		got, err := record.ReadRaw(&raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Requests) != rep.Requests.Count {
			t.Errorf("raw has %d requests, the report %d", len(got.Requests), rep.Requests.Count)
		}
	})
}

// A healthy server never trips the rules, however long the run is. It runs on a fake clock, so the
// run lasts exactly as long as the Plan says and the count of requests is exact: nothing depends on how fast
// the machine is (on Windows, a run was measured 0.3 ms short of its Duration).
func TestRunNotAbortedWhenHealthy(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		const (
			latency  = 10 * time.Millisecond
			duration = 10*time.Second + 5*time.Millisecond // ends in the middle of an iteration
		)
		stub := fakeServerAfter(latency)
		rep, err := run(context.Background(), Plan{
			Scenario: getScenario(), BaseURL: "http://stub.test", Users: 2, Duration: duration,
			AbortOn: []AbortRule{{When: "http_5xx_rate>50%"}, {When: "p99>10s"}},
		}, stub)
		if err != nil {
			t.Fatal(err)
		}
		// Each user starts an iteration every 10ms from 0 to 10s inclusive (1001 of them); the last one
		// ends at 10.01s.
		if rep.Requests.Count != 2*1001 || stub.calls.Load() != 2*1001 {
			t.Errorf("requests = %d (stub saw %d), want %d", rep.Requests.Count, stub.calls.Load(), 2*1001)
		}
		if want := 10*time.Second + latency; rep.Elapsed != want {
			t.Errorf("Elapsed = %v, want %v", rep.Elapsed, want)
		}
	})
}

// After an abort the running iterations get Grace counted from the abort, not from the end of Duration,
// so one hanging request does not keep Run waiting for Duration + Grace (here: 60s + 30s).
func TestRunAbortGraceStartsAtAbort(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		server := &fakeServer{reply: func(r *http.Request) fakeReply {
			if r.URL.Path == "/hang" {
				return fakeReply{Delay: 24 * time.Hour} // ended only by the context
			}
			return fakeReply{Delay: 7 * time.Millisecond, Status: http.StatusInternalServerError}
		}}
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("get", func(s *Step) error {
				if u.ID == 1 {
					return s.Get("/hang").Do().ExpectStatus(200)
				}
				return s.Get("/bad").Do().ExpectStatus(200)
			})
		})
		rep, err := run(context.Background(), Plan{
			Scenario: sc, BaseURL: "http://stub.test", Users: 2, Duration: time.Minute, Grace: 30 * time.Second,
			Timeout: time.Hour, // longer than anything here, so only the Grace can end the hanging request
			AbortOn: []AbortRule{{When: "http_5xx_rate>50%"}},
		}, server)
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
		// Aborted at the first evaluation (1s); the hanging request is cancelled when the Grace that started
		// then is over.
		if want := 31 * time.Second; rep.Elapsed != want {
			t.Errorf("Elapsed = %v, want %v (abort at 1s plus Grace)", rep.Elapsed, want)
		}
		if it := rep.Scenarios[0].Iterations; it.Errors[record.ErrCanceled] != 1 {
			t.Errorf("iterations = %+v, want the hanging one recorded as canceled", it)
		}
	})
}
