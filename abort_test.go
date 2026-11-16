package jotaro

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fastAbort shortens the evaluation interval for the duration of a test.
func fastAbort(t *testing.T) {
	t.Helper()
	old := abortInterval
	abortInterval = 20 * time.Millisecond
	t.Cleanup(func() { abortInterval = old })
}

// failingServer answers 500 to everything.
func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
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

func TestRunAbortsEarly(t *testing.T) {
	fastAbort(t)
	srv := failingServer(t)

	for name, plan := range map[string]Plan{
		"closed": {Users: 2},
		"staged": {UserStages: []UserStage{{Target: 2, For: time.Second}}},
		"open":   {Arrival: Rate(200, time.Second)},
	} {
		t.Run(name, func(t *testing.T) {
			var progress syncBuffer
			plan.Scenario, plan.BaseURL, plan.Duration = getScenario(), srv.URL, 30*time.Second
			plan.AbortOn = []AbortRule{{When: "http_5xx_rate>50%", For: 100 * time.Millisecond}}
			plan.Progress = &progress

			begin := time.Now()
			rep, err := Run(context.Background(), plan)
			if !errors.Is(err, ErrAborted) {
				t.Fatalf("err = %v, want ErrAborted", err)
			}
			if d := time.Since(begin); d > 5*time.Second {
				t.Errorf("Run took %v although the condition held almost at once", d)
			}
			if rep == nil || rep.Requests.Count == 0 {
				t.Fatalf("partial report missing: %+v", rep)
			}
			if rep.Elapsed >= 5*time.Second {
				t.Errorf("Elapsed = %v", rep.Elapsed)
			}
			if !strings.Contains(err.Error(), "http_5xx_rate>50%") {
				t.Errorf("error does not name the condition: %v", err)
			}
			if !strings.Contains(progress.String(), "aborted: http_5xx_rate>50%") {
				t.Errorf("no abort line in progress output: %q", progress.String())
			}
		})
	}
}

// The raw data of an aborted run is still written.
func TestRunAbortWithRaw(t *testing.T) {
	fastAbort(t)
	srv := failingServer(t)

	var raw bytes.Buffer
	rep, err := Run(context.Background(), Plan{
		Scenario: getScenario(), BaseURL: srv.URL, Users: 2, Duration: 30 * time.Second,
		AbortOn: []AbortRule{{When: "http_5xx_rate>50%"}}, Raw: &raw,
	})
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if raw.Len() == 0 || rep.Requests.Count == 0 {
		t.Errorf("raw = %d bytes, requests = %d", raw.Len(), rep.Requests.Count)
	}
}

func TestRunNotAbortedWhenHealthy(t *testing.T) {
	fastAbort(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rep, err := Run(context.Background(), Plan{
		Scenario: getScenario(), BaseURL: srv.URL, Users: 2, Duration: 300 * time.Millisecond,
		AbortOn: []AbortRule{{When: "http_5xx_rate>50%"}, {When: "p99>10s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Requests.Count == 0 || rep.Elapsed < 300*time.Millisecond {
		t.Errorf("run was cut short: %d requests in %v", rep.Requests.Count, rep.Elapsed)
	}
}

// After an abort the running iterations get Grace counted from the abort, not from the end of Duration,
// so one hanging request does not keep Run waiting for Duration + Grace.
func TestRunAbortGraceStartsAtAbort(t *testing.T) {
	fastAbort(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error {
			if u.ID == 1 {
				return s.Get("/hang").Do().ExpectStatus(200)
			}
			return s.Get("/bad").Do().ExpectStatus(200)
		})
	})
	begin := time.Now()
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Users: 2, Duration: 30 * time.Second, Grace: 300 * time.Millisecond,
		AbortOn: []AbortRule{{When: "http_5xx_rate>50%"}},
	})
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("Run took %v; the hanging request was not cancelled after Grace", d)
	}
	if rep.Requests.Count == 0 {
		t.Error("no requests recorded")
	}
}
