package jotaro

import (
	"context"
	"fmt"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// Scenario is the flow that a virtual user runs once.
type Scenario struct {
	Name string
	Fn   func(ctx context.Context, u *User) error
}

// NewScenario creates a scenario.
func NewScenario(name string, fn func(ctx context.Context, u *User) error) *Scenario {
	return &Scenario{Name: name, Fn: fn}
}

// run runs the scenario once and leaves a record.IterationRecord.
// If Fn panics, the iteration is treated as failed and the whole run keeps going.
func (sc *Scenario) run(u *User) (err error) {
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("jotaro: scenario panicked: %v", p)
		}
		u.col.AddIteration(record.IterationRecord{
			Scenario:  sc.Name,
			UserID:    u.ID,
			Iteration: u.Iteration,
			Start:     start,
			Duration:  time.Since(start),
			Think:     u.think,
			Err:       classify(err),
		})
	}()
	return sc.Fn(u.ctx, u)
}
