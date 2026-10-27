package jotaro

import (
	"sync"
	"testing"
	"time"
)

func TestCollectorConcurrent(t *testing.T) {
	c := NewCollector()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.AddRequest(Record{})
			c.AddStep(StepRecord{})
			c.AddIteration(IterationRecord{})
		}()
	}
	wg.Wait()

	reqs, steps, iters := c.Snapshot()
	if len(reqs) != 50 || len(steps) != 50 || len(iters) != 50 {
		t.Fatalf("got %d/%d/%d, want 50/50/50", len(reqs), len(steps), len(iters))
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	c := NewCollector()
	c.AddRequest(Record{Code: 200})
	reqs, _, _ := c.Snapshot()
	reqs[0].Code = 500

	again, _, _ := c.Snapshot()
	if again[0].Code != 200 {
		t.Fatalf("Snapshot が内部のスライスを共有している: %d", again[0].Code)
	}
}

func TestIterationActive(t *testing.T) {
	r := IterationRecord{Duration: 3 * time.Second, Think: time.Second}
	if got := r.Active(); got != 2*time.Second {
		t.Fatalf("Active() = %v, want 2s", got)
	}
}
