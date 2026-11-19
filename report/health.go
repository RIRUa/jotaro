package report

import (
	"fmt"

	"github.com/RIRUa/jotaro/record"
)

// mergeHealth adds the points in parts into one, each shifted by the matching offset in seconds.
// Goroutine counts are summed and CPU takes the maximum (with several load generators, the most saturated one matters).
func mergeHealth(parts [][]record.HealthPoint, offsets []int) []record.HealthPoint {
	return mergeBySecond(parts, offsets,
		func(p record.HealthPoint) int { return p.Second }, func(p *record.HealthPoint, s int) { p.Second = s },
		func(q *record.HealthPoint, p record.HealthPoint) {
			q.Goroutines += p.Goroutines
			q.CPU = max(q.CPU, p.CPU)
		})
}

// healthCPUWarn is the CPU usage at or above which the load generator is suspected to be saturated.
const healthCPUWarn = 0.9

// healthSummary summarizes Health.
type healthSummary struct {
	HasCPU        bool    // whether any point has CPU data
	MaxCPU        float64 // 0 to 1
	AvgCPU        float64 // 0 to 1
	MaxGoroutines int
}

// Hot reports whether the CPU is close to its limit, so the load generator may be saturated and the measurements worse than reality.
func (h healthSummary) Hot() bool { return h.HasCPU && h.MaxCPU >= healthCPUWarn }

// summarizeHealth summarizes the points. ok is false when there are none.
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

// String has the form "cpu max 62.1% (avg 41.0%), goroutines max 1234".
func (h healthSummary) String() string {
	cpu := "cpu n/a"
	if h.HasCPU {
		cpu = fmt.Sprintf("cpu max %.1f%% (avg %.1f%%)", h.MaxCPU*100, h.AvgCPU*100)
	}
	return fmt.Sprintf("%s, goroutines max %d", cpu, h.MaxGoroutines)
}

// warning is the note shown when Hot.
func (h healthSummary) warning() string {
	return fmt.Sprintf("load generator CPU reached %.0f%%: measured latencies may be worse than the target's actual behavior", h.MaxCPU*100)
}
