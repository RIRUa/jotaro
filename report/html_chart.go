package report

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// Charts are emitted as inline SVG, with no external library or JavaScript.

// Chart geometry in SVG user units: the full size, then the margins
// (left / right, top / bottom) around the plot area.
const (
	chartW, chartH = 640, 200
	chartL, chartR = 48, 12 // left / right margins (the left one holds the Y-axis labels)
	chartT, chartB = 10, 24
)

// htmlTick is one axis tick: where it sits and what it says.
type htmlTick struct {
	Pos   string // position in the SVG
	Label string
}

// htmlLine is one series drawn as a line. A gap in the data splits it into several polylines.
type htmlLine struct {
	Class  string   // l1 to l3; the colors are set in CSS
	Name   string   // legend
	Points []string // points of one polyline per continuous run
}

// htmlChart is one SVG line chart, with every coordinate already computed for the template.
type htmlChart struct {
	Title  string
	W, H   int
	Left   int
	Right  int
	Top    int
	Bottom int
	YTicks []htmlTick
	XTicks []htmlTick
	Lines  []htmlLine
}

// seriesCharts builds line charts from the time series. It returns nil when there is no series.
func seriesCharts(pts []SeriesPoint) []htmlChart {
	if len(pts) == 0 {
		return nil
	}
	n := pts[len(pts)-1].Second + 1

	build := func(title string, names []string, valid func(SeriesPoint) bool, val func(SeriesPoint) []float64) htmlChart {
		cols := make([]seriesCol, 0, len(pts))
		for _, p := range pts {
			if valid(p) {
				cols = append(cols, seriesCol{p.Second, val(p)})
			}
		}
		return lineChart(title, names, n, cols)
	}

	always := func(SeriesPoint) bool { return true }
	hasReq := func(p SeriesPoint) bool { return p.Requests > 0 }
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

	charts := []htmlChart{
		build("requests / s", []string{"requests", "errors", "5xx"}, always,
			func(p SeriesPoint) []float64 {
				return []float64{float64(p.Requests), float64(p.Errors), float64(p.HTTP5xx)}
			}),
		build("latency (ms, by request start)", []string{"p50", "p95", "p99"}, hasReq,
			func(p SeriesPoint) []float64 {
				return []float64{ms(p.Latency.P50), ms(p.Latency.P95), ms(p.Latency.P99)}
			}),
		build("concurrency (active iterations, peak)", []string{"iterations"}, always,
			func(p SeriesPoint) []float64 { return []float64{float64(p.Concurrency)} }),
	}
	return append(charts, connCharts(pts, build)...)
}

// connPhase is one phase of opening a connection (DNS, TCP, TLS).
type connPhase struct {
	name string
	get  func(SeriesPoint) Latency
}

// connCharts builds the connection charts. With no connection records (for example old JSON) it returns nothing.
func connCharts(pts []SeriesPoint, build func(title string, names []string, valid func(SeriesPoint) bool, val func(SeriesPoint) []float64) htmlChart) []htmlChart {
	var charts []htmlChart
	if slices.ContainsFunc(pts, func(p SeriesPoint) bool { return p.NewConns+p.ReusedConns > 0 }) {
		charts = append(charts, build("connections / s (by request start)", []string{"new", "reused"},
			func(p SeriesPoint) bool { return p.NewConns+p.ReusedConns > 0 },
			func(p SeriesPoint) []float64 { return []float64{float64(p.NewConns), float64(p.ReusedConns)} }))
	}

	// Only phases that happened at least once become lines (not DNS for a bare IP, not TLS over plain http, and so on).
	phases := []connPhase{
		{"dns", func(p SeriesPoint) Latency { return p.DNS }},
		{"connect", func(p SeriesPoint) Latency { return p.Connect }},
		{"tls", func(p SeriesPoint) Latency { return p.TLS }},
	}
	phases = slices.DeleteFunc(phases, func(ph connPhase) bool {
		return !slices.ContainsFunc(pts, func(p SeriesPoint) bool { return ph.get(p).Count > 0 })
	})
	if len(phases) == 0 {
		return charts
	}
	names := make([]string, len(phases))
	for i, ph := range phases {
		names[i] = ph.name
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return append(charts, build("connection setup (ms, p95, new connections only)", names,
		func(p SeriesPoint) bool {
			return slices.ContainsFunc(phases, func(ph connPhase) bool { return ph.get(p).Count > 0 })
		},
		func(p SeriesPoint) []float64 {
			vals := make([]float64, len(phases))
			for i, ph := range phases {
				if l := ph.get(p); l.Count > 0 {
					vals[i] = ms(l.P95)
				} else {
					vals[i] = math.NaN() // a phase that did not happen in that second breaks the line
				}
			}
			return vals
		}))
}

// healthCharts builds the goroutine and CPU charts of the load generator. It returns nil when there are no records.
func healthCharts(pts []record.HealthPoint) []htmlChart {
	if len(pts) == 0 {
		return nil
	}
	n := pts[len(pts)-1].Second + 1

	goroutines := make([]seriesCol, 0, len(pts))
	var cpu []seriesCol
	for _, p := range pts {
		goroutines = append(goroutines, seriesCol{p.Second, []float64{float64(p.Goroutines)}})
		if p.CPU >= 0 {
			cpu = append(cpu, seriesCol{p.Second, []float64{p.CPU * 100}})
		}
	}
	charts := []htmlChart{lineChart("load generator goroutines", []string{"goroutines"}, n, goroutines)}
	if len(cpu) > 0 {
		charts = append(charts, lineChart("load generator CPU (% of GOMAXPROCS)", []string{"cpu"}, n, cpu))
	}
	return charts
}

// seriesCol is the value of each series for one second. A series with no value is NaN.
type seriesCol struct {
	sec  int
	vals []float64
}

// lineChart draws the values of each series as a line, with x running over seconds 0..n-1.
// The line is broken where seconds are skipped (no data).
func lineChart(title string, names []string, n int, cols []seriesCol) htmlChart {
	maxV := 0.0
	for _, c := range cols {
		for _, v := range c.vals {
			if !math.IsNaN(v) {
				maxV = math.Max(maxV, v)
			}
		}
	}
	step := niceStep(maxV)
	top := math.Ceil(maxV/step) * step
	if top == 0 {
		top = step
	}

	plotW, plotH := float64(chartW-chartL-chartR), float64(chartH-chartT-chartB)
	x := func(sec int) float64 {
		if n <= 1 {
			return float64(chartL) + plotW/2
		}
		return float64(chartL) + plotW*float64(sec)/float64(n-1)
	}
	y := func(v float64) float64 { return float64(chartT) + plotH*(1-v/top) }
	pos := func(f float64) string { return fmt.Sprintf("%.1f", f) }

	ch := htmlChart{Title: title, W: chartW, H: chartH, Left: chartL, Right: chartW - chartR, Top: chartT, Bottom: chartH - chartB}
	for v := 0.0; v <= top+step/2; v += step {
		ch.YTicks = append(ch.YTicks, htmlTick{Pos: pos(y(v)), Label: fmtNum(v)})
	}
	xs := niceStep(float64(n - 1))
	for s := 0.0; s <= float64(n-1)+xs/2 && int(s) < n; s += xs {
		ch.XTicks = append(ch.XTicks, htmlTick{Pos: pos(x(int(s))), Label: fmt.Sprintf("%ds", int(s))})
	}

	for i, name := range names {
		line := htmlLine{Class: fmt.Sprintf("l%d", i+1), Name: name}
		var run []string
		prev := -2
		flush := func() {
			if len(run) == 0 {
				return
			}
			if len(run) == 1 {
				run = append(run, run[0]) // a run of a single point is drawn as a dot
			}
			line.Points = append(line.Points, strings.Join(run, " "))
			run = nil
		}
		for _, c := range cols {
			sec := c.sec
			if sec != prev+1 {
				flush()
			}
			prev = sec
			if math.IsNaN(c.vals[i]) { // a second with no value; break the line
				flush()
				prev = -2
				continue
			}
			run = append(run, pos(x(sec))+","+pos(y(c.vals[i])))
		}
		flush()
		ch.Lines = append(ch.Lines, line)
	}
	return ch
}

// niceStep returns a step of 1, 2 or 5 × 10^k that divides max into about 4 intervals.
func niceStep(max float64) float64 {
	if max <= 0 {
		return 1
	}
	raw := max / 4
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	switch f := raw / mag; {
	case f <= 1:
		return mag
	case f <= 2:
		return 2 * mag
	case f <= 5:
		return 5 * mag
	}
	return 10 * mag
}

// fmtNum formats an axis label without a trailing ".0" for whole numbers.
func fmtNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}
