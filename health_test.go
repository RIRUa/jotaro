package jotaro

import (
	"context"
	"testing"
	"time"
)

func TestHealthSampler(t *testing.T) {
	h := startHealthSampler(20 * time.Millisecond)

	// Add goroutines (blocked) and use CPU too (spin only one).
	stop := make(chan struct{})
	for range 20 {
		go func() { <-stop }()
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			default: //nolint:staticcheck // spin on purpose to use CPU
			}
		}
	}()
	time.Sleep(150 * time.Millisecond)
	pts := h.stop()
	close(stop)

	if len(pts) < 2 {
		t.Fatalf("too few points: %+v", pts)
	}
	_, hasCPU := processCPUTime()
	for i, p := range pts {
		if p.Second != i {
			t.Errorf("pts[%d].Second = %d", i, p.Second)
		}
		if p.Goroutines < 20 {
			t.Errorf("pts[%d].Goroutines = %d, want at least 20", i, p.Goroutines)
		}
		switch {
		case !hasCPU && p.CPU != -1:
			t.Errorf("pts[%d].CPU = %v, want -1 where it cannot be obtained", i, p.CPU)
		case hasCPU && (p.CPU < 0 || p.CPU > 1):
			t.Errorf("pts[%d].CPU = %v, want within 0 to 1", i, p.CPU)
		}
	}
	if last := pts[len(pts)-1]; hasCPU && last.CPU <= 0 {
		t.Errorf("CPU = %v despite a busy loop", last.CPU)
	}
}

func TestHealthSamplerShortRun(t *testing.T) {
	h := startHealthSampler(time.Hour)
	time.Sleep(5 * time.Millisecond)
	// Even if shorter than half an interval, one last point is kept rather than none.
	if pts := h.stop(); len(pts) != 1 || pts[0].Second != 0 || pts[0].Goroutines < 1 {
		t.Errorf("pts = %+v", pts)
	}
}

// The health sampler reads the real CPU time of the process (getrusage), which a fake clock does not
// move, so on a fake clock the CPU values mean nothing and only the sampling schedule is checked here:
// one point per second of the run (TestHealthSampler covers the values on the real clock).
func TestRunRecordsHealth(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			u.Think(10 * time.Millisecond)
			return nil
		})
		rep, err := Run(context.Background(), Plan{Scenario: sc, Users: 2, Duration: 5*time.Second + 100*time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Health) != 5 {
			t.Fatalf("got %d health points, want 5 (one per second, the last 0.1s is dropped as shorter than half a second)", len(rep.Health))
		}
		for i, p := range rep.Health {
			if p.Second != i || p.Goroutines < 1 {
				t.Errorf("Health[%d] = %+v", i, p)
			}
		}
	})
}
