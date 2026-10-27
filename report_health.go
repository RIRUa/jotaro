package jotaro

import (
	"fmt"
	"runtime"
	"slices"
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

// mergeHealth は offset 秒ずらした b を a に足し込む。
// ゴルーチン数は合計し、CPU は最大を取る（負荷生成側が複数あるときは、一番詰まっているものを見るため）。
func mergeHealth(a, b []HealthPoint, offset int) []HealthPoint {
	out := slices.Clone(a)
	for _, p := range b {
		sec := p.Second + offset
		i := slices.IndexFunc(out, func(q HealthPoint) bool { return q.Second == sec })
		if i < 0 {
			p.Second = sec
			out = append(out, p)
			continue
		}
		out[i].Goroutines += p.Goroutines
		out[i].CPU = max(out[i].CPU, p.CPU)
	}
	slices.SortFunc(out, func(x, y HealthPoint) int { return x.Second - y.Second })
	return out
}

// healthCPUWarn は、これ以上の CPU 使用率を「詰まっている疑いがある」とみなす境目。
const healthCPUWarn = 0.9

// healthSummary は Health の要約。
type healthSummary struct {
	HasCPU        bool    // CPU を取得できた点があるか
	MaxCPU        float64 // 0〜1
	AvgCPU        float64 // 0〜1
	MaxGoroutines int
}

// Hot は CPU が上限に近く、負荷生成側が詰まって測定値が悪く出ている疑いがあるか。
func (h healthSummary) Hot() bool { return h.HasCPU && h.MaxCPU >= healthCPUWarn }

// summarizeHealth は点を要約する。点が無ければ ok が false。
func summarizeHealth(pts []HealthPoint) (h healthSummary, ok bool) {
	if len(pts) == 0 {
		return h, false
	}
	var sum float64
	n := 0
	for _, p := range pts {
		h.MaxGoroutines = max(h.MaxGoroutines, p.Goroutines)
		if p.CPU < 0 {
			continue
		}
		h.HasCPU = true
		h.MaxCPU = max(h.MaxCPU, p.CPU)
		sum += p.CPU
		n++
	}
	if n > 0 {
		h.AvgCPU = sum / float64(n)
	}
	return h, true
}

// String は "cpu max 62.1% (avg 41.0%), goroutines max 1234" の形。
func (h healthSummary) String() string {
	cpu := "cpu n/a"
	if h.HasCPU {
		cpu = fmt.Sprintf("cpu max %.1f%% (avg %.1f%%)", h.MaxCPU*100, h.AvgCPU*100)
	}
	return fmt.Sprintf("%s, goroutines max %d", cpu, h.MaxGoroutines)
}

// warning は Hot のときの注意書き。
func (h healthSummary) warning() string {
	return fmt.Sprintf("load generator CPU reached %.0f%%: measured latencies may be worse than the target's actual behavior", h.MaxCPU*100)
}
