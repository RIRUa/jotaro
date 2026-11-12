package jotaro

import (
	"errors"
	"fmt"
	"time"
)

// maxStageUsers is the largest Target a UserStage may have. It guards against a typo
// (an extra zero) allocating a goroutine and a channel for each of a billion users.
const maxStageUsers = 1_000_000

// UserStage is one stage of Plan.UserStages. During the stage, the number of active users moves
// linearly from the Target of the previous stage (Plan.StartUsers, 0 by default, for the first stage) to Target over For.
// Set Target to the previous Target to hold the number of users.
//
// For example, ramp up to 100 users over 30 seconds, hold for 60 seconds, then ramp down to 0 over 10 seconds:
//
//	UserStages: []jotaro.UserStage{
//		{Target: 100, For: 30 * time.Second},
//		{Target: 100, For: 60 * time.Second},
//		{Target: 0, For: 10 * time.Second},
//	},
type UserStage struct {
	// Target is the number of active users at the end of the stage.
	Target int
	// For is how long the stage lasts.
	For time.Duration
}

// validateUserStages requires a StartUsers within 0 and maxStageUsers, at least one stage, a non-negative Target
// not above maxStageUsers and a positive For on every stage, and at least one user ever running
// (a positive StartUsers or a stage with a positive Target).
func validateUserStages(start int, stages []UserStage) error {
	switch {
	case start < 0:
		return errors.New("jotaro: Plan.StartUsers must be at least 0")
	case start > maxStageUsers:
		return fmt.Errorf("jotaro: Plan.StartUsers must be at most %d", maxStageUsers)
	case len(stages) == 0:
		return errors.New("jotaro: Plan.UserStages needs at least one stage")
	}
	positive := start > 0
	for i, st := range stages {
		switch {
		case st.Target < 0:
			return fmt.Errorf("jotaro: Target of stage %d of Plan.UserStages must be at least 0", i+1)
		case st.Target > maxStageUsers:
			return fmt.Errorf("jotaro: Target of stage %d of Plan.UserStages must be at most %d", i+1, maxStageUsers)
		case st.For <= 0:
			return fmt.Errorf("jotaro: For of stage %d of Plan.UserStages must be positive", i+1)
		}
		positive = positive || st.Target > 0
	}
	if !positive {
		return errors.New("jotaro: Plan.UserStages with every Target 0 never runs a user")
	}
	return nil
}

// maxStageTarget is the largest of start and the Targets of the stages: how many users must be allocated.
func maxStageTarget(start int, stages []UserStage) int {
	m := start
	for _, st := range stages {
		m = max(m, st.Target)
	}
	return m
}

// userChange is a moment at which the number of active users becomes count.
type userChange struct {
	at    time.Duration // since the start of the run
	count int
}

// userChanges lays the stages out as the changes of the active user count, in time order.
// The count is start before the first stage.
//
// Within a stage that moves from a to b over d, every step of one user is placed at an equal
// distance, so the count reaches b exactly when the stage ends. For example, 0 to 4 over 8 seconds
// activates one user at 2, 4, 6 and 8 seconds. A holding stage (a == b) has no changes.
// After the last stage the count stays at its Target.
func userChanges(start int, stages []UserStage) []userChange {
	var (
		out []userChange
		off time.Duration // start of the current stage
		cur = start
	)
	for _, st := range stages {
		a, b, d := cur, st.Target, st.For
		if a != b {
			step := 1
			if b < a {
				step = -1
			}
			n := (b - a) * step // number of single-user steps in this stage
			for i := 1; i <= n; i++ {
				at := off + time.Duration(float64(d)*float64(i)/float64(n))
				out = append(out, userChange{at: at, count: a + i*step})
			}
		}
		off += d
		cur = b
	}
	return out
}
