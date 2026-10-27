package jotaro

import (
	"runtime"
	"time"
)

// HealthPoint は負荷生成側（jotaro を動かしているプロセス）の、1 秒ごとの健全性。
// CPU が上限に近いときは、生成側が詰まって測定値が実際より悪く出ている疑いがある。
type HealthPoint struct {
	// Second は Report.StartedAt からの経過秒（0 始まり）。区間は [Second, Second+1) 秒。
	Second int
	// Goroutines は区間の終わりの時点のゴルーチン数。
	Goroutines int
	// CPU は区間の間にプロセスが使った CPU 時間の割合（0〜1）。GOMAXPROCS 個の CPU 全体に対する割合。
	// 負荷をかける対象を同じプロセスで動かしているときは、その分も含む。
	// CPU 時間を取得できない環境（Unix 以外）では -1。
	CPU float64
}

// healthSampler は interval ごとにゴルーチン数と CPU 使用率を記録する。
type healthSampler struct {
	interval time.Duration
	done     chan struct{}
	points   chan []HealthPoint
}

// startHealthSampler は記録を始める。stop を呼ぶと止まり、それまでの記録を返す。
func startHealthSampler(interval time.Duration) *healthSampler {
	h := &healthSampler{interval: interval, done: make(chan struct{}), points: make(chan []HealthPoint, 1)}
	go h.run()
	return h
}

func (h *healthSampler) run() {
	var pts []HealthPoint
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	lastAt := time.Now()
	lastCPU, _ := processCPUTime()
	sample := func(now time.Time) {
		p := HealthPoint{Second: len(pts), Goroutines: runtime.NumGoroutine(), CPU: -1}
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
func (h *healthSampler) stop() []HealthPoint {
	close(h.done)
	return <-h.points
}
