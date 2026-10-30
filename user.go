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
	// scheduled は予定到着時刻。最初のリクエストが使い切ると 0 に戻る。
	scheduled time.Time
}

// Step runs one section of the scenario and records its duration and outcome.
// The error returned by fn is returned as is, so the caller decides whether to continue or stop.
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
	err := fn(s)
	u.col.AddStep(record.StepRecord{
		Scenario:  u.scenario,
		Step:      name,
		UserID:    u.ID,
		Iteration: u.Iteration,
		Start:     start,
		Duration:  time.Since(start),
		Err:       classify(err),
	})
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
