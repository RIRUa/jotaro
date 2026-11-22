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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

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
	p := Plan{Scenario: sc, BaseURL: srv.URL, Arrival: Rate(200, time.Second), Duration: 300 * time.Millisecond}
	rep, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Requests.Count == 0 || rep.Requests.Count%perStep != 0 {
		t.Fatalf("requests = %d, want a positive multiple of %d", rep.Requests.Count, perStep)
	}
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("page", func(s *Step) error {
			return s.Parallel(
				func() error { return s.Get("/a").Do().ExpectStatus(200) },
				func() error { return s.Get("/b").Do().ExpectStatus(200) },
				func() error { return s.Get("/c").Do().ExpectStatus(200) },
			)
		})
	})
	p := Plan{Scenario: sc, BaseURL: srv.URL, Arrival: Rate(20, time.Second), Duration: 200 * time.Millisecond}
	ctx := context.Background()
	r := &runner{p: p, tr: http.DefaultTransport, timeout: time.Second, col: record.NewCollector(), stopCtx: ctx, runCtx: ctx}
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	r.stopCtx = stopCtx
	r.runOpen()

	reqs, _, _ := r.col.Snapshot()
	if len(reqs) == 0 || len(reqs)%3 != 0 {
		t.Fatalf("requests = %d, want a positive multiple of 3", len(reqs))
	}
	// Per iteration (the UserID is the arrival number) at most one request is scheduled before it started.
	// A request that took the arrival time may also start exactly on time, so the count is only bounded above.
	early := map[int]int{}
	for _, rec := range reqs {
		if rec.Scheduled.Before(rec.Start) {
			early[rec.UserID]++
		}
	}
	for id, n := range early {
		if n > 1 {
			t.Fatalf("iteration %d: %d requests carry the scheduled time, want at most 1", id, n)
		}
	}
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
}

func TestParallelNoFunctions(t *testing.T) {
	if err := (&Step{}).Parallel(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestParallelRepanicsOnCaller(t *testing.T) {
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
}

// A panic in a Parallel function is recorded like a panic in the step function.
func TestParallelPanicRecordedAsStepPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("page", func(s *Step) error {
			return s.Parallel(
				func() error { return s.Get("/").Do().ExpectStatus(200) },
				func() error { panic("boom") },
			)
		})
	})
	rep, err := Run(context.Background(), Plan{Scenario: sc, BaseURL: srv.URL, Users: 1, Duration: 100 * time.Millisecond, Progress: discard{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Scenarios) != 1 || len(rep.Scenarios[0].Steps) != 1 {
		t.Fatalf("unexpected report shape: %+v", rep.Scenarios)
	}
	st := rep.Scenarios[0].Steps[0]
	if st.Failed != st.Count || st.Count == 0 {
		t.Fatalf("step count=%d failed=%d, want every run failed", st.Count, st.Failed)
	}
	if st.Errors[record.ErrPanic] != st.Count {
		t.Fatalf("step errors = %v, want %d panics", st.Errors, st.Count)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
