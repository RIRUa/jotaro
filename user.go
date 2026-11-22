package jotaro

import (
	"context"
	"net/http"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// User is a virtual user that runs the scenario once.
// ID is the number of the virtual user and does not change across iterations.
// Client has its own cookie jar per iteration, so every iteration starts a fresh session.
//
// A User belongs to the goroutine that runs the scenario: call [User.Step] and [User.Think] only from it.
// To send requests concurrently, do it inside one step with [Step.Parallel] (or your own goroutines
// that finish before the step function returns); [Step] is safe for that.
type User struct {
	// ID identifies the virtual user. In the open model every arrival gets its own ID.
	ID int
	// Iteration counts how many times this user has run the scenario (0-based).
	Iteration int
	// Client sends the requests. Use it directly for anything the Step builders do not cover,
	// but note that such requests are not recorded.
	Client *http.Client

	ctx      context.Context
	scenario string
	baseURL  string
	col      *record.Collector

	// think is the total time spent in Think so far, subtracted to get the active time.
	think time.Duration
	// scheduled is the scheduled arrival time. It goes back to zero once the first request has used it.
	scheduled schedSlot
}

// Step runs one section of the scenario and records its duration and outcome.
// The error returned by fn is returned as is, so the caller decides whether to continue or stop.
// If fn panics, the step is recorded as failed with the error kind "panic" and the panic continues.
func (u *User) Step(name string, fn func(s *Step) error) error {
	s := &Step{
		name: name,
		exec: &executor{
			ctx:     u.ctx,
			client:  u.Client,
			baseURL: u.baseURL,
			tag: record.Record{
				Scenario:  u.scenario,
				Step:      name,
				UserID:    u.ID,
				Iteration: u.Iteration,
			},
			sched: &u.scheduled,
			sink:  u.col.AddRequest,
		},
	}

	start := time.Now()
	rec := record.StepRecord{
		Scenario:  u.scenario,
		Step:      name,
		UserID:    u.ID,
		Iteration: u.Iteration,
		Start:     start,
	}
	// If fn panics, the step is still recorded (failed with record.ErrPanic, with the time up to the panic)
	// and the panic is raised again, so Scenario.run handles the iteration as before.
	defer func() {
		if p := recover(); p != nil {
			rec.Duration = time.Since(start)
			rec.Err = record.ErrPanic
			u.col.AddStep(rec)
			panic(p)
		}
	}()
	err := fn(s)
	rec.Duration = time.Since(start)
	rec.Err = classify(err)
	u.col.AddStep(rec)
	return err
}

// Think waits for d. The time waited is counted in neither the step nor Active.
// It returns early when ctx is done.
func (u *User) Think(d time.Duration) {
	if d <= 0 {
		return
	}
	start := time.Now()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-u.ctx.Done():
	}
	u.think += time.Since(start)
}
