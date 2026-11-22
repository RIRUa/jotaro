package jotaro

import (
	"context"
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
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
		})
		// 100/s for 60s (and 5ms, so that no arrival ties with the end): one arrival every 10ms from 0 to 60s.
		rep, err := run(context.Background(), Plan{
			Scenario: sc, BaseURL: "http://stub.test", Arrival: Rate(100, time.Second), Duration: time.Minute + 5*time.Millisecond,
		}, fakeServerAfter(3*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Open || rep.Dropped != 0 {
			t.Fatalf("Open=%v Dropped=%d", rep.Open, rep.Dropped)
		}
		if n := rep.Scenarios[0].Iterations.Count; n != 6001 {
			t.Fatalf("Iterations = %d, want 6001", n)
		}
		if rep.Scenarios[0].Iterations.Failed != 0 {
			t.Fatalf("Failed = %d", rep.Scenarios[0].Iterations.Failed)
		}
		// Every full second has exactly 100 requests (the 61st holds only the arrival at 60s).
		if len(rep.Series) != 61 {
			t.Fatalf("Series has %d points, want 61", len(rep.Series))
		}
		for _, p := range rep.Series[:60] {
			if p.Requests != 100 {
				t.Errorf("second %d has %d requests, want 100", p.Second, p.Requests)
			}
		}
	})
}

// When responses are slow and the concurrency cap is reached, arrivals are dropped.
func TestRunRateDropped(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
		})
		// An arrival every 20ms and a request that takes 190ms with room for one: the arrival at 0 runs, the
		// nine at 20..180ms are dropped, the one at 200ms runs again, and so on. Arrivals go from 0 to 4000ms.
		rep, err := run(context.Background(), Plan{
			Scenario: sc, BaseURL: "http://stub.test", Arrival: Rate(50, time.Second),
			MaxUsers: 1, Duration: 4*time.Second + 10*time.Millisecond,
		}, fakeServerAfter(190*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if rep.Dropped != 180 {
			t.Errorf("Dropped = %d, want 180", rep.Dropped)
		}
		if n := rep.Scenarios[0].Iterations.Count; n != 21 {
			t.Errorf("Iterations = %d, want 21", n)
		}
		if want := 4*time.Second + 190*time.Millisecond; rep.Elapsed != want {
			t.Errorf("Elapsed = %v, want %v", rep.Elapsed, want)
		}
	})
}

// The scheduled arrival time is set only on the first request of an iteration.
func TestRunRateScheduled(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			u.Think(10 * time.Millisecond) // the iteration starts its first request 10ms after it was scheduled
			for _, name := range []string{"a", "b"} {
				if err := u.Step(name, func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) }); err != nil {
					return err
				}
				u.Think(20 * time.Millisecond)
			}
			return nil
		})
		p := Plan{Scenario: sc, BaseURL: "http://stub.test", Arrival: Rate(20, time.Second), Duration: 205 * time.Millisecond}
		ctx := context.Background()
		stopCtx, stop := context.WithTimeout(ctx, p.Duration)
		defer stop()
		r := &runner{p: p, tr: fakeServerAfter(3 * time.Millisecond), timeout: time.Second, col: record.NewCollector(), stopCtx: stopCtx, runCtx: ctx}
		r.runOpen()

		// An arrival every 50ms from 0 to 200ms (the end is at 205ms): 5 iterations of 2 requests.
		reqs, _, _ := r.col.Snapshot()
		if len(reqs) != 10 {
			t.Fatalf("requests = %d, want 10", len(reqs))
		}
		for _, rec := range reqs {
			switch rec.Step {
			case "a": // The first request carries the arrival time: the dispatcher was on time, the Think was not.
				if got := rec.Start.Sub(rec.Scheduled); got != 10*time.Millisecond {
					t.Errorf("a: Start - Scheduled = %v, want 10ms", got)
				}
			case "b": // Later requests of the iteration have no scheduled time of their own.
				if !rec.Scheduled.Equal(rec.Start) {
					t.Errorf("b: Scheduled should equal Start, differs by %v", rec.Start.Sub(rec.Scheduled))
				}
			}
		}
	})
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
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
		})
		// 0 -> 200/s over 10 seconds: N(t) = 10*t^2 arrivals, 1000 by the end of the ramp (plus the one at 0).
		rep, err := run(context.Background(), Plan{
			Scenario: sc, BaseURL: "http://stub.test", Arrival: Ramp(0, 200, time.Second, 10*time.Second),
			Duration: 10*time.Second + 2500*time.Microsecond,
		}, fakeServerAfter(3*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if n := rep.Scenarios[0].Iterations.Count; n != 1001 {
			t.Fatalf("Iterations = %d, want 1001", n)
		}
		// Second s of the ramp has N(s+1) - N(s) = 10*(2s+1) arrivals; one may move across a boundary.
		for _, p := range rep.Series[:10] {
			if want := 10 * (2*p.Second + 1); p.Requests < want-1 || p.Requests > want+1 {
				t.Errorf("second %d has %d requests, want %d", p.Second, p.Requests, want)
			}
		}
	})
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

// TestRampDescendingBounds checks that a descending Ramp stays monotonic,
// non-negative and within over for every arrival, including the last ones where the
// discriminant of the quadratic formula used to round below 0 and produce NaN.
func TestRampDescendingBounds(t *testing.T) {
	cases := []struct {
		name      string
		from, to  int
		per, over time.Duration
	}{
		{"11to0 over 10s", 11, 0, time.Second, 10 * time.Second},
		{"5to0 over 300ms per 1s", 5, 0, 300 * time.Millisecond, 3 * time.Second},
		{"10to0 over 10s", 10, 0, time.Second, 10 * time.Second},
		{"100to0 over 1s", 100, 0, time.Second, time.Second},
		{"50to3 over 30s", 50, 3, time.Second, 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Ramp(tc.from, tc.to, tc.per, tc.over)
			prev := time.Duration(0)
			for i := 0; i < 2000; i++ {
				got, ok := a.at(i)
				if !ok {
					break
				}
				// to > 0 continues past over at a constant rate, so only the ramp part is bounded.
				if got < prev || got < 0 || (tc.to == 0 && got > tc.over) {
					t.Fatalf("at(%d) = %v (prev %v), want within [prev, %v]", i, got, prev, tc.over)
				}
				prev = got
			}
		})
	}
}

func TestRampDescendingStageCount(t *testing.T) {
	// 11 → 0/s over 10s is 55 arrivals; one extra arrival used to start immediately.
	st := Stage{Ramp(11, 0, time.Second, 10*time.Second), 10 * time.Second}
	if got := st.count(); got != 55 {
		t.Errorf("count = %d, want 55", got)
	}
	// 5 per 300ms over 3s: the integral is 5·(3s/300ms)/2 = 25 arrivals.
	st = Stage{Ramp(5, 0, 300*time.Millisecond, 3*time.Second), 3 * time.Second}
	if got := st.count(); got != 25 {
		t.Errorf("count = %d, want 25", got)
	}
}

// TestRampDescendingSweep sweeps from and over to catch rounding in the final arrivals.
func TestRampDescendingSweep(t *testing.T) {
	overs := []time.Duration{time.Second, 3 * time.Second, 7 * time.Second, 10 * time.Second, 30 * time.Second}
	for from := 1; from <= 300; from++ {
		for _, over := range overs {
			a := Ramp(from, 0, time.Second, over)
			prev := time.Duration(0)
			for i := 0; i < from*int(over/time.Second)+10; i++ {
				got, ok := a.at(i)
				if !ok {
					break
				}
				if got < prev || got < 0 || got > over {
					t.Fatalf("Ramp(%d, 0, 1s, %v).at(%d) = %v (prev %v)", from, over, i, got, prev)
				}
				prev = got
			}
			// An arrival is dropped by Stage.count only if it does not fit in over, so count must not exceed
			// the exact integral from·over/2 by more than the arrival on its boundary.
			want := float64(from) * over.Seconds() / 2
			if got := (Stage{a, over}).count(); float64(got) > want+1 || float64(got) < want-1 {
				t.Fatalf("Ramp(%d, 0, 1s, %v) count = %d, want about %v", from, over, got, want)
			}
		}
	}
}

func FuzzRampAt(f *testing.F) {
	f.Add(11, 0, int64(time.Second), int64(10*time.Second))
	f.Add(5, 0, int64(300*time.Millisecond), int64(3*time.Second))
	f.Add(0, 10, int64(time.Second), int64(10*time.Second))
	f.Add(10, 10, int64(time.Second), int64(10*time.Second))
	f.Add(50, 3, int64(time.Second), int64(30*time.Second))
	f.Fuzz(func(t *testing.T, from, to int, per, over int64) {
		a := Ramp(from%1000, to%1000, time.Duration(per), time.Duration(over))
		if a.validate() != nil {
			t.Skip()
		}
		prev := time.Duration(0)
		for i := 0; i < 3000; i++ {
			got, ok := a.at(i)
			if !ok {
				return
			}
			if got < prev || got < 0 {
				t.Fatalf("at(%d) = %v (prev %v)", i, got, prev)
			}
			prev = got
		}
	})
}
