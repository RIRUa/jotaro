package jotaro

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// Several goroutines of one step send requests in the open model. This is the scenario of issue #131:
// under -race it reported a data race on the scheduled arrival time. Nothing here depends on Step.Parallel,
// so it covers the plain-goroutine style too.
func TestRunOpenConcurrentRequestsInStep(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		const perStep = 4
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("assets", func(s *Step) error {
				var wg sync.WaitGroup
				for i := 0; i < perStep; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); s.Get("/a").Do() }()
				}
				wg.Wait()
				return nil
			})
		})
		// An arrival every 5ms from 0 to 3s inclusive: 601 iterations of 4 requests each.
		p := Plan{Scenario: sc, BaseURL: "http://stub.test", Arrival: Rate(200, time.Second), Duration: 3*time.Second + 2*time.Millisecond}
		rep, err := run(context.Background(), p, fakeServerAfter(3*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if rep.Requests.Count != 601*perStep {
			t.Fatalf("requests = %d, want %d", rep.Requests.Count, 601*perStep)
		}
	})
}

// Of the requests that start together, exactly one takes the scheduled arrival time.
func TestExecutorSchedTakenOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, nil)
	e.sched = &schedSlot{}
	at := time.Now().Add(-time.Second)
	e.sched.set(at)

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); newRequest(e, "GET", "/").Do() }()
	}
	wg.Wait()

	if len(*recs) != n {
		t.Fatalf("records = %d, want %d", len(*recs), n)
	}
	taken := 0
	for _, r := range *recs {
		if r.Scheduled.Equal(at) {
			taken++
		} else if !r.Scheduled.Equal(r.Start) {
			t.Fatalf("Scheduled = %v, want Start %v or the arrival time", r.Scheduled, r.Start)
		}
	}
	if taken != 1 {
		t.Fatalf("%d requests took the scheduled time, want 1", taken)
	}
}

// A Step inside a real iteration: with Parallel, only the first request carries the dispatch delay.
func TestParallelOpenModelCorrectedOnce(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			u.Think(10 * time.Millisecond) // the step starts 10ms after the arrival it was scheduled for
			return u.Step("page", func(s *Step) error {
				return s.Parallel(
					func() error { return s.Get("/a").Do().ExpectStatus(200) },
					func() error { return s.Get("/b").Do().ExpectStatus(200) },
					func() error { return s.Get("/c").Do().ExpectStatus(200) },
				)
			})
		})
		p := Plan{Scenario: sc, BaseURL: "http://stub.test", Arrival: Rate(20, time.Second), Duration: 205 * time.Millisecond}
		ctx := context.Background()
		stopCtx, stop := context.WithTimeout(ctx, p.Duration)
		defer stop()
		r := &runner{p: p, tr: fakeServerAfter(3 * time.Millisecond), timeout: time.Second, col: record.NewCollector(), stopCtx: stopCtx, runCtx: ctx}
		r.runOpen()

		// An arrival every 50ms from 0 to 200ms: 5 iterations of 3 requests.
		reqs, _, _ := r.col.Snapshot()
		if len(reqs) != 15 {
			t.Fatalf("requests = %d, want 15", len(reqs))
		}
		// Every iteration is 10ms late (the Think), and exactly one of its three requests carries that delay:
		// the one that took the scheduled time. The other two have no scheduled time of their own. A slot that
		// were not cleared after the first request would give all three the delay.
		delayed := map[int]int{}
		for _, rec := range reqs {
			switch got := rec.Start.Sub(rec.Scheduled); got {
			case 10 * time.Millisecond:
				delayed[rec.UserID]++
			case 0:
			default:
				t.Errorf("iteration %d: Start - Scheduled = %v, want 10ms or 0", rec.UserID, got)
			}
		}
		if len(delayed) != 5 {
			t.Errorf("%d iterations have a delayed request, want 5: %v", len(delayed), delayed)
		}
		for id, n := range delayed {
			if n != 1 {
				t.Errorf("iteration %d: %d requests carry the scheduled time, want exactly 1", id, n)
			}
		}
	})
}

func TestParallelRunsAllConcurrently(t *testing.T) {
	s := &Step{name: "s"}
	// Every function waits until all have started, so this deadlocks if they ran one after another.
	const n = 5
	var ready sync.WaitGroup
	ready.Add(n)
	fns := make([]func() error, n)
	for i := range fns {
		fns[i] = func() error {
			ready.Done()
			ready.Wait()
			return nil
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.Parallel(fns...) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Parallel did not run its functions concurrently")
	}
}

func TestParallelJoinsErrorsAndWaitsForAll(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		s := &Step{name: "s"}
		e1, e2 := errors.New("e1"), errors.New("e2")
		var finished bool
		err := s.Parallel(
			func() error { return e1 },
			func() error { time.Sleep(50 * time.Millisecond); finished = true; return nil },
			func() error { return e2 },
		)
		if !finished {
			t.Fatal("Parallel returned before every function finished")
		}
		if !errors.Is(err, e1) || !errors.Is(err, e2) {
			t.Fatalf("err = %v, want both e1 and e2", err)
		}
		if got, want := err.Error(), "e1\ne2"; got != want {
			t.Fatalf("err = %q, want %q (order of fns)", got, want)
		}
	})
}

func TestParallelNoFunctions(t *testing.T) {
	if err := (&Step{}).Parallel(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestParallelRepanicsOnCaller(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		s := &Step{name: "s"}
		var finished bool
		defer func() {
			if p := recover(); p != "boom" {
				t.Fatalf("recovered %v, want boom", p)
			}
			if !finished {
				t.Fatal("Parallel panicked before the other function finished")
			}
		}()
		s.Parallel(
			func() error { panic("boom") },
			func() error { time.Sleep(50 * time.Millisecond); finished = true; return nil },
		)
		t.Fatal("Parallel should have panicked")
	})
}

// A panic in a Parallel function is recorded like a panic in the step function.
func TestParallelPanicRecordedAsStepPanic(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("page", func(s *Step) error {
				return s.Parallel(
					func() error { return s.Get("/").Do().ExpectStatus(200) },
					func() error { panic("boom") },
				)
			})
		})
		rep, err := run(context.Background(), Plan{Scenario: sc, BaseURL: "http://stub.test", Users: 1, Duration: 100 * time.Millisecond, Progress: discard{}}, fakeServerAfter(3*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Scenarios) != 1 || len(rep.Scenarios[0].Steps) != 1 {
			t.Fatalf("unexpected report shape: %+v", rep.Scenarios)
		}
		// An iteration every 3ms from 0 to 99ms: 34, every one of them with the panic.
		st := rep.Scenarios[0].Steps[0]
		if st.Count != 34 || st.Failed != st.Count {
			t.Fatalf("step count=%d failed=%d, want 34 runs, every one failed", st.Count, st.Failed)
		}
		if st.Errors[record.ErrPanic] != st.Count {
			t.Fatalf("step errors = %v, want %d panics", st.Errors, st.Count)
		}
	})
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
