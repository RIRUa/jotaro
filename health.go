package jotaro

import (
	"runtime"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// healthSampler records the goroutine count and CPU usage every interval.
type healthSampler struct {
	interval time.Duration
	done     chan struct{}
	points   chan []record.HealthPoint
}

// startHealthSampler starts recording. Calling stop ends it and returns what was recorded.
func startHealthSampler(interval time.Duration) *healthSampler {
	h := &healthSampler{interval: interval, done: make(chan struct{}), points: make(chan []record.HealthPoint, 1)}
	go h.run()
	return h
}

// run is the sampling loop, run in its own goroutine until done is closed.
// CPU is the CPU time used since the previous sample divided by the wall time
// and by GOMAXPROCS, clamped to [0, 1]; it stays -1 where CPU time is unavailable.
func (h *healthSampler) run() {
	var pts []record.HealthPoint
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	lastAt := time.Now()
	lastCPU, _ := processCPUTime()
	sample := func(now time.Time) {
		p := record.HealthPoint{Second: len(pts), Goroutines: runtime.NumGoroutine(), CPU: -1}
		cpu, ok := processCPUTime()
		if wall := now.Sub(lastAt); ok && wall > 0 {
			p.CPU = min(max(float64(cpu-lastCPU)/(float64(wall)*float64(runtime.GOMAXPROCS(0))), 0), 1)
		}
		pts = append(pts, p)
		lastAt, lastCPU = now, cpu
	}
	for {
		select {
		case now := <-ticker.C:
			sample(now)
		case <-h.done:
			// Record the last partial interval too if it is at least half long, so even a short run leaves one point.
			if now := time.Now(); len(pts) == 0 || now.Sub(lastAt) >= h.interval/2 {
				sample(now)
			}
			h.points <- pts
			return
		}
	}
}

// stop ends the recording and returns the points.
func (h *healthSampler) stop() []record.HealthPoint {
	close(h.done)
	return <-h.points
}
