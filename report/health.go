package report

import (
	"fmt"
	"slices"

	"github.com/RIRUa/jotaro/record"
)

// mergeHealth は offset 秒ずらした b を a に足し込む。
// ゴルーチン数は合計し、CPU は最大を取る（負荷生成側が複数あるときは、一番詰まっているものを見るため）。
func mergeHealth(a, b []record.HealthPoint, offset int) []record.HealthPoint {
	out := slices.Clone(a)
	for _, p := range b {
		sec := p.Second + offset
		i := slices.IndexFunc(out, func(q record.HealthPoint) bool { return q.Second == sec })
		if i < 0 {
			p.Second = sec
			out = append(out, p)
			continue
		}
		out[i].Goroutines += p.Goroutines
		out[i].CPU = max(out[i].CPU, p.CPU)
	}
	slices.SortFunc(out, func(x, y record.HealthPoint) int { return x.Second - y.Second })
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
func summarizeHealth(pts []record.HealthPoint) (h healthSummary, ok bool) {
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
