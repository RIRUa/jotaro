package record

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
		t.Fatalf("Snapshot shares the internal slice: %d", again[0].Code)
	}
}

func TestIterationActive(t *testing.T) {
	r := IterationRecord{Duration: 3 * time.Second, Think: time.Second}
	if got := r.Active(); got != 2*time.Second {
		t.Fatalf("Active() = %v, want 2s", got)
	}
}

// obsCount is an Observer that only counts.
type obsCount struct {
	mu                          sync.Mutex
	reqs, steps, iters, started int
}

func (o *obsCount) ObserveRequest(Record)            { o.mu.Lock(); o.reqs++; o.mu.Unlock() }
func (o *obsCount) ObserveStep(StepRecord)           { o.mu.Lock(); o.steps++; o.mu.Unlock() }
func (o *obsCount) ObserveIteration(IterationRecord) { o.mu.Lock(); o.iters++; o.mu.Unlock() }
func (o *obsCount) ObserveIterationStart(time.Time)  { o.mu.Lock(); o.started++; o.mu.Unlock() }

// A streaming Collector hands every record to the Observer and keeps none of them.
func TestStreamingCollectorKeepsNothing(t *testing.T) {
	o := &obsCount{}
	c := NewStreamingCollector(o)
	for i := 0; i < 10; i++ {
		c.StartIteration(time.Now())
		c.AddRequest(Record{})
		c.AddStep(StepRecord{})
		c.AddIteration(IterationRecord{})
	}
	c.AddDropped()

	if o.reqs != 10 || o.steps != 10 || o.iters != 10 || o.started != 10 {
		t.Errorf("observer saw %d/%d/%d/%d, want 10 of each", o.reqs, o.steps, o.iters, o.started)
	}
	reqs, steps, iters := c.Take()
	if len(reqs)+len(steps)+len(iters) != 0 {
		t.Errorf("a streaming Collector kept %d/%d/%d records", len(reqs), len(steps), len(iters))
	}
	if c.Dropped() != 1 {
		t.Errorf("Dropped = %d, want 1", c.Dropped())
	}
}

// A Collector that keeps records ignores StartIteration (it needs no Observer).
func TestStartIterationWithoutObserver(t *testing.T) {
	NewCollector().StartIteration(time.Now())
}
