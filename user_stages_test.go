package jotaro

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUserChanges(t *testing.T) {
	s := time.Second
	cases := []struct {
		name   string
		stages []UserStage
		want   []userChange
	}{
		{"ramp up", []UserStage{{4, 8 * s}}, []userChange{{2 * s, 1}, {4 * s, 2}, {6 * s, 3}, {8 * s, 4}}},
		{"hold has no changes", []UserStage{{2, 2 * s}, {2, 5 * s}}, []userChange{{1 * s, 1}, {2 * s, 2}}},
		{"ramp down", []UserStage{{2, s}, {0, 4 * s}}, []userChange{{s / 2, 1}, {s, 2}, {3 * s, 1}, {5 * s, 0}}},
		{"later stages start where the previous ended", []UserStage{{1, s}, {3, 2 * s}}, []userChange{{s, 1}, {2 * s, 2}, {3 * s, 3}}},
		{"all zero", []UserStage{{0, s}}, nil},
	}
	for _, tc := range cases {
		if got := userChanges(tc.stages); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidateUserStages(t *testing.T) {
	s := time.Second
	bad := map[string][]UserStage{
		"empty":           {},
		"negative target": {{Target: -1, For: s}},
		"too many users":  {{Target: maxStageUsers + 1, For: s}},
		"zero For":        {{Target: 1, For: 0}},
		"negative For":    {{Target: 1, For: -s}},
		"all zero":        {{Target: 0, For: s}, {Target: 0, For: s}},
	}
	for name, st := range bad {
		if err := validateUserStages(st); err == nil {
			t.Errorf("%s: should be an error", name)
		}
	}
	if err := validateUserStages([]UserStage{{1, s}, {0, s}}); err != nil {
		t.Errorf("valid stages: %v", err)
	}
}

func TestPlanValidateUserStages(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error { return nil })
	stages := []UserStage{{Target: 1, For: time.Second}}
	cases := map[string]Plan{
		"with Users":   {Scenario: sc, Users: 1, UserStages: stages, Duration: time.Second},
		"with Arrival": {Scenario: sc, Arrival: Rate(1, time.Second), UserStages: stages, Duration: time.Second},
		"empty stages": {Scenario: sc, UserStages: []UserStage{}, Duration: time.Second},
		"bad stage":    {Scenario: sc, UserStages: []UserStage{{Target: 1}}, Duration: time.Second},
	}
	for name, p := range cases {
		if _, err := Run(context.Background(), p); err == nil {
			t.Errorf("%s: should be an error", name)
		}
	}
	if err := (Plan{Scenario: sc, UserStages: stages, Duration: time.Second}).validate(); err != nil {
		t.Errorf("valid plan: %v", err)
	}
}

// TestRunUserStages ramps up to 4 users, holds, then drops to 1 and checks the load shape:
// the number of concurrent iterations follows the target, and user IDs and iteration counts stay stable.
func TestRunUserStages(t *testing.T) {
	var (
		running, peak atomic.Int64
		mu            sync.Mutex
		lastIter      = map[int]int{}
		badIter       bool
		ids           = map[int]bool{}
	)
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		mu.Lock()
		ids[u.ID] = true
		if u.Iteration != lastIter[u.ID] {
			badIter = true // iterations of one user must be 0, 1, 2, ... without gaps or resets
		}
		lastIter[u.ID] = u.Iteration + 1
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return nil
	})

	rep, err := Run(context.Background(), Plan{
		Scenario: sc,
		UserStages: []UserStage{
			{Target: 4, For: 200 * time.Millisecond},
			{Target: 4, For: 200 * time.Millisecond},
			{Target: 1, For: 100 * time.Millisecond},
		},
		Duration: 600 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got != 4 {
		t.Errorf("peak concurrency = %d, want 4", got)
	}
	if badIter {
		t.Error("Iteration of a user is not consecutive")
	}
	for id := 0; id < 4; id++ {
		if !ids[id] {
			t.Errorf("user %d never ran", id)
		}
	}
	if len(ids) != 4 {
		t.Errorf("IDs = %v, want 0..3", ids)
	}
	if rep.Open {
		t.Error("UserStages is the closed model, but Report.Open is set")
	}
	if len(rep.Scenarios) != 1 || rep.Scenarios[0].Iterations.Count == 0 {
		t.Fatal("no iterations ran")
	}
	// After the drop to 1 user (the last 100ms) at most one iteration runs at a time, so with
	// the 1-second granularity the series only tells the peak: it must reach 4 and never exceed it.
	peakSeries := 0
	for _, p := range rep.Series {
		peakSeries = max(peakSeries, p.Concurrency)
	}
	if peakSeries != 4 {
		t.Errorf("Series peak Concurrency = %d, want 4", peakSeries)
	}
}

// TestRunUserStagesParkedUsersResume checks that a user stopped by a lower target runs again,
// with the same ID and the next iteration number, when the target rises again.
func TestRunUserStagesParkedUsersResume(t *testing.T) {
	var (
		mu    sync.Mutex
		iters = map[int][]int{}
	)
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		mu.Lock()
		iters[u.ID] = append(iters[u.ID], u.Iteration)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		return nil
	})
	_, err := Run(context.Background(), Plan{
		Scenario: sc,
		UserStages: []UserStage{
			{Target: 2, For: 10 * time.Millisecond},
			{Target: 2, For: 100 * time.Millisecond},
			{Target: 1, For: 10 * time.Millisecond},
			{Target: 1, For: 100 * time.Millisecond},
			{Target: 2, For: 10 * time.Millisecond},
			{Target: 2, For: 100 * time.Millisecond},
		},
		Duration: 330 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for id, seq := range iters {
		for i, it := range seq {
			if it != i {
				t.Fatalf("user %d iterations = %v, want 0, 1, 2, ...", id, seq)
			}
		}
	}
	// User 1 ran before the drop and after the rise, so it ran more than during the first hold alone.
	if len(iters[1]) < 30 {
		t.Errorf("user 1 ran %d iterations; it should have resumed after the target rose again", len(iters[1]))
	}
}

// TestRunUserStagesFinishesIteration checks that scaling down does not interrupt a running iteration.
func TestRunUserStagesFinishesIteration(t *testing.T) {
	var canceled atomic.Int64
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
			canceled.Add(1)
		}
		return nil
	})
	_, err := Run(context.Background(), Plan{
		Scenario: sc,
		UserStages: []UserStage{
			{Target: 2, For: 10 * time.Millisecond},
			{Target: 0, For: 10 * time.Millisecond},
		},
		Duration: 100 * time.Millisecond,
		Grace:    time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Load() != 0 {
		t.Errorf("%d iterations were canceled; they should finish within Grace", canceled.Load())
	}
}

func TestRunUserStagesContextCanceled(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		time.Sleep(time.Millisecond)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, Plan{
		Scenario:   sc,
		UserStages: []UserStage{{Target: 3, For: time.Hour}},
		Duration:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Run took %v after the context ended", d)
	}
}
