package jotaro

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// グラフは外部ライブラリも JavaScript も使わず、インライン SVG として出力する。

const (
	chartW, chartH = 640, 200
	chartL, chartR = 48, 12 // 左右の余白（左は Y 軸の目盛り用）
	chartT, chartB = 10, 24
)

type htmlTick struct {
	Pos   string // SVG 上の座標
	Label string
}

type htmlLine struct {
	Class  string   // l1〜l3。色は CSS で決める
	Name   string   // 凡例
	Points []string // 連続した区間ごとの polyline の points
}

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

// seriesCharts は時系列から折れ線グラフを作る。時系列が無ければ nil。
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

// connPhase は接続を張るときの区間（DNS・TCP・TLS）のひとつ。
type connPhase struct {
	name string
	get  func(SeriesPoint) Latency
}

// connCharts は接続のグラフ。接続の記録が無ければ（古い JSON など）何も返さない。
func connCharts(pts []SeriesPoint, build func(title string, names []string, valid func(SeriesPoint) bool, val func(SeriesPoint) []float64) htmlChart) []htmlChart {
	var charts []htmlChart
	if slices.ContainsFunc(pts, func(p SeriesPoint) bool { return p.NewConns+p.ReusedConns > 0 }) {
		charts = append(charts, build("connections / s (by request start)", []string{"new", "reused"},
			func(p SeriesPoint) bool { return p.NewConns+p.ReusedConns > 0 },
			func(p SeriesPoint) []float64 { return []float64{float64(p.NewConns), float64(p.ReusedConns)} }))
	}

	// 区間が一度でも起きた（DNS が無い IP 直指定、http の TLS など）ものだけ線にする。
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
					vals[i] = math.NaN() // その秒に起きなかった区間は線を切る
				}
			}
			return vals
		}))
}

// seriesCol は 1 秒分の、系列ごとの値。値が無い系列は NaN。
type seriesCol struct {
	sec  int
	vals []float64
}

// lineChart は x が 0..n-1 秒、系列ごとの値を折れ線にする。
// 秒が飛んでいる（データが無い）ところでは線を切る。
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
				run = append(run, run[0]) // 1 点だけの区間は丸い点として描く
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
			if math.IsNaN(c.vals[i]) { // 値が無い秒。線を切る
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

// niceStep は max を 4 区間前後に割る、1・2・5 × 10^k の刻み幅を返す。
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

func fmtNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}
