package jotaro

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// Scenario is the flow that a virtual user runs once.
type Scenario struct {
	// Name labels the scenario in every record and in the report.
	Name string
	// Fn is the flow itself. Returning an error marks the iteration as failed.
	Fn func(ctx context.Context, u *User) error
}

// NewScenario creates a scenario.
func NewScenario(name string, fn func(ctx context.Context, u *User) error) *Scenario {
	return &Scenario{Name: name, Fn: fn}
}

// panicError is the error of an iteration whose scenario function panicked.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string { return fmt.Sprintf("jotaro: scenario panicked: %v", e.value) }

// run runs the scenario once and leaves a record.IterationRecord.
// If Fn panics, the iteration is treated as failed and the whole run keeps going.
func (sc *Scenario) run(u *User) (err error) {
	start := time.Now()
	u.col.StartIteration(start)
	defer func() {
		if p := recover(); p != nil {
			err = &panicError{value: p, stack: debug.Stack()}
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
