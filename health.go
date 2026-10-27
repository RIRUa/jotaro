package jotaro

import (
	"runtime"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// healthSampler は interval ごとにゴルーチン数と CPU 使用率を記録する。
type healthSampler struct {
	interval time.Duration
	done     chan struct{}
	points   chan []record.HealthPoint
}

// startHealthSampler は記録を始める。stop を呼ぶと止まり、それまでの記録を返す。
func startHealthSampler(interval time.Duration) *healthSampler {
	h := &healthSampler{interval: interval, done: make(chan struct{}), points: make(chan []record.HealthPoint, 1)}
	go h.run()
	return h
}

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
			// 最後の中途半端な区間も、半分以上あれば記録する。短い実行でも 1 点は残すため。
			if now := time.Now(); len(pts) == 0 || now.Sub(lastAt) >= h.interval/2 {
				sample(now)
			}
			h.points <- pts
			return
		}
	}
}

// stop は記録を止めて、点を返す。
func (h *healthSampler) stop() []record.HealthPoint {
	close(h.done)
	return <-h.points
}
