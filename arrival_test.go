package jotaro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func TestRateValidate(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error { return nil })
	cases := map[string]Plan{
		"together with Users": {Scenario: sc, Users: 1, Arrival: Rate(1, time.Second), Duration: time.Second},
		"count is 0":          {Scenario: sc, Arrival: Rate(0, time.Second), Duration: time.Second},
		"period is 0":         {Scenario: sc, Arrival: Rate(1, 0), Duration: time.Second},
		"Ramp both 0":         {Scenario: sc, Arrival: Ramp(0, 0, time.Second, time.Second), Duration: time.Second},
		"Ramp period is 0":    {Scenario: sc, Arrival: Ramp(0, 1, 0, time.Second), Duration: time.Second},
		"Ramp slope is 0":     {Scenario: sc, Arrival: Ramp(0, 1, time.Second, 0), Duration: time.Second},
		"Ramp negative":       {Scenario: sc, Arrival: Ramp(-1, 1, time.Second, time.Second), Duration: time.Second},
		"MaxUsers negative":   {Scenario: sc, Arrival: Rate(1, time.Second), MaxUsers: -1, Duration: time.Second},
	}
	for name, p := range cases {
		if _, err := Run(context.Background(), p); err == nil {
			t.Errorf("%s: should be an error", name)
		}
	}
}

func TestRunRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Arrival: Rate(100, time.Second), Duration: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Open || rep.Dropped != 0 {
		t.Fatalf("Open=%v Dropped=%d", rep.Open, rep.Dropped)
	}
	// 100/s × 0.5s = 50 times. Allow a margin for timer jitter (especially large on Windows).
	if n := rep.Scenarios[0].Iterations.Count; n < 40 || n > 56 {
		t.Fatalf("Iterations = %d, want about 50", n)
	}
	if rep.Scenarios[0].Iterations.Failed != 0 {
		t.Fatalf("Failed = %d", rep.Scenarios[0].Iterations.Failed)
	}
}

// When responses are slow and the concurrency cap is reached, arrivals are dropped.
func TestRunRateDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Arrival: Rate(50, time.Second),
		MaxUsers: 1, Duration: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped == 0 {
		t.Fatal("drops should be recorded")
	}
	if n := rep.Scenarios[0].Iterations.Count; n > 3 {
		t.Fatalf("Iterations = %d, want about 2 with MaxUsers=1", n)
	}
}

// The scheduled arrival time is set only on the first request of an iteration.
func TestRunRateScheduled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		for _, name := range []string{"a", "b"} {
			if err := u.Step(name, func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) }); err != nil {
				return err
			}
			u.Think(20 * time.Millisecond)
		}
		return nil
	})
	p := Plan{Scenario: sc, BaseURL: srv.URL, Arrival: Rate(20, time.Second), Duration: 200 * time.Millisecond}
	ctx := context.Background()
	r := &runner{p: p, tr: http.DefaultTransport, timeout: time.Second, col: record.NewCollector(), stopCtx: ctx, runCtx: ctx}
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	r.stopCtx = stopCtx
	r.runOpen()

	reqs, _, _ := r.col.Snapshot()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for _, rec := range reqs {
		switch rec.Step {
		case "a": // If late it is before Start; if on time it is the same as or slightly before Start.
			if rec.Scheduled.After(rec.Start) {
				t.Errorf("a: Scheduled is after Start: %v", rec.Scheduled.Sub(rec.Start))
			}
		case "b":
			if !rec.Scheduled.Equal(rec.Start) {
				t.Errorf("b: Scheduled should equal Start")
			}
		}
	}
}

func TestRecordCorrected(t *testing.T) {
	now := time.Now()
	r := record.Record{Scheduled: now, Start: now.Add(300 * time.Millisecond), Latency: 50 * time.Millisecond}
	if got := r.Corrected(); got != 350*time.Millisecond {
		t.Fatalf("Corrected = %v", got)
	}
	if got := (record.Record{Latency: 5}).Corrected(); got != 5 {
		t.Fatalf("should return Latency when Scheduled is unset: %v", got)
	}
}

func TestRampAt(t *testing.T) {
	near := func(got time.Duration, ok bool, want time.Duration) bool {
		return ok && (got-want).Abs() < time.Millisecond
	}
	// 0 → 10/s over 10 seconds. N(t) = t²/2, so the i-th arrival is at sqrt(2i) seconds. The total over the ramp is 50.
	up := Ramp(0, 10, time.Second, 10*time.Second)
	for i, want := range map[int]time.Duration{0: 0, 2: 2 * time.Second, 8: 4 * time.Second, 50: 10 * time.Second, 60: 11 * time.Second} {
		if got, ok := up.at(i); !near(got, ok, want) {
			t.Errorf("up.at(%d) = %v, %v, want %v", i, got, ok, want)
		}
	}
	// 10 → 10/s is the same as Rate.
	flat := Ramp(10, 10, time.Second, 10*time.Second)
	if got, ok := flat.at(25); !near(got, ok, 2500*time.Millisecond) {
		t.Errorf("flat.at(25) = %v, %v", got, ok)
	}
	// 10 → 0/s over 10 seconds. No arrivals after the 50th.
	down := Ramp(10, 0, time.Second, 10*time.Second)
	if got, ok := down.at(50); !near(got, ok, 10*time.Second) {
		t.Errorf("down.at(50) = %v, %v", got, ok)
	}
	if _, ok := down.at(51); ok {
		t.Error("down.at(51) should have no arrival")
	}
	// Times increase monotonically.
	prev := time.Duration(-1)
	for i := 0; i < 200; i++ {
		got, _ := up.at(i)
		if got < prev {
			t.Fatalf("at(%d) = %v < %v", i, got, prev)
		}
		prev = got
	}
}

func TestRunRamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	// 0 → 200/s over 1 second: 100 in total.
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Arrival: Ramp(0, 200, time.Second, time.Second), Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Allow a margin for timer jitter (especially large on Windows).
	if n := rep.Scenarios[0].Iterations.Count; n < 80 || n > 110 {
		t.Fatalf("Iterations = %d, want about 100", n)
	}
}

func TestStagesAt(t *testing.T) {
	// 0 to 2s: 10/s (20 times), 2 to 4s: 20/s (40 times).
	s := Stages(
		Stage{Rate(10, time.Second), 2 * time.Second},
		Stage{Rate(20, time.Second), 2 * time.Second},
	)
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]time.Duration{0: 0, 19: 1900 * time.Millisecond, 20: 2 * time.Second, 22: 2100 * time.Millisecond, 59: 3950 * time.Millisecond} {
		if got, ok := s.at(i); !ok || got != want {
			t.Errorf("at(%d) = %v, %v, want %v", i, got, ok, want)
		}
	}
	if _, ok := s.at(60); ok {
		t.Error("there should be no arrivals after all stages")
	}
}

func TestStagesRampHoldDown(t *testing.T) {
	// 0→10/s for 4s (20 times), 10/s for 2s (20 times), 10→0/s for 4s (20 times).
	s := Stages(
		Stage{Ramp(0, 10, time.Second, 4*time.Second), 4 * time.Second},
		Stage{Rate(10, time.Second), 2 * time.Second},
		Stage{Ramp(10, 0, time.Second, 4*time.Second), 4 * time.Second},
	)
	n := 0
	prev := time.Duration(-1)
	for ; ; n++ {
		got, ok := s.at(n)
		if !ok {
			break
		}
		if got < prev || got >= 10*time.Second {
			t.Fatalf("at(%d) = %v (previous: %v)", n, got, prev)
		}
		prev = got
	}
	if n < 59 || n > 61 {
		t.Errorf("arrivals = %d, want about 60", n)
	}
}

func TestStagesValidate(t *testing.T) {
	bad := map[string]Arrival{
		"empty":         Stages(),
		"For is 0":      Stages(Stage{Rate(1, time.Second), 0}),
		"no Arrival":    Stages(Stage{nil, time.Second}),
		"invalid stage": Stages(Stage{Rate(0, time.Second), time.Second}),
	}
	for name, a := range bad {
		if a.validate() == nil {
			t.Errorf("%s: should be an error", name)
		}
	}
}
