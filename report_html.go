package jotaro

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"
)

//go:embed report.html.tmpl
var reportHTMLSrc string

var reportHTML = template.Must(template.New("report").Parse(reportHTMLSrc))

// 表示用の型。テンプレートが計算をしなくて済むよう、文字列と割合は先に作る。

type htmlBar struct {
	Label string
	Value string
	Pct   float64 // 0〜100。最大値に対する割合
}

type htmlCount struct {
	Name  string
	Count int
}

type htmlLatencyRow struct {
	Label string
	Cells []string // min, mean, p50, p90, p95, p99, max
}

type htmlStep struct {
	Name     string
	Count    int
	Failed   int
	Duration []string
	Requests int
	Status   string
	Errors   string
	P50      string
	P95      string
	P99      string
	TTFBP50  string
	TTFBP95  string
}

type htmlScenario struct {
	Name       string
	Iterations int
	PerSecond  string
	Failed     int
	Errors     string
	Latency    []htmlLatencyRow
	Steps      []htmlStep
}

type htmlView struct {
	Version   int
	Model     string
	Elapsed   string
	Requests  int
	RPS       string
	Open      bool
	Dropped   int
	Bytes     string
	Status    []htmlCount
	Errors    []htmlCount
	Latency   []htmlBar // リクエストのレイテンシ分布
	Corrected []htmlBar // Open のときだけ
	TTFB      []htmlBar
	Scenarios []htmlScenario
}

var latencyHeaders = []string{"min", "mean", "p50", "p90", "p95", "p99", "max"}

// WriteHTML はレポートを、外部に依存しない 1 枚の HTML として w に書き出す。
func (r *Report) WriteHTML(w io.Writer) error {
	return reportHTML.Execute(w, r.toHTMLView())
}

func (r *Report) toHTMLView() htmlView {
	v := htmlView{
		Version:  JSONVersion,
		Model:    "closed (Users)",
		Elapsed:  fmtDur(r.Elapsed),
		Requests: r.Requests.Count,
		RPS:      fmt.Sprintf("%.2f", r.RPS()),
		Open:     r.Open,
		Dropped:  r.Dropped,
		Bytes:    fmt.Sprintf("in %s / out %s", fmtBytes(r.Requests.BytesIn), fmtBytes(r.Requests.BytesOut)),
		Status:   sortedCounts(r.Requests.Status),
		Errors:   sortedCounts(errCounts(r.Requests.Errors)),
		Latency:  latencyBars(r.Requests.Latency),
		TTFB:     latencyBars(r.Requests.TTFB),
	}
	if r.Open {
		v.Model = "open (Rate)"
		v.Corrected = latencyBars(r.Requests.Corrected)
	}
	for _, sc := range r.Scenarios {
		hs := htmlScenario{
			Name:       sc.Name,
			Iterations: sc.Iterations.Count,
			PerSecond:  fmt.Sprintf("%.2f", perSecond(sc.Iterations.Count, r.Elapsed)),
			Failed:     sc.Iterations.Failed,
			Errors:     fmtErrors(sc.Iterations.Errors),
			Latency: []htmlLatencyRow{
				{Label: "active (excl. think)", Cells: latencyCellList(sc.Iterations.Active)},
				{Label: "total", Cells: latencyCellList(sc.Iterations.Total)},
			},
		}
		for _, s := range sc.Steps {
			q := s.Requests
			hs.Steps = append(hs.Steps, htmlStep{
				Name: s.Name, Count: s.Count, Failed: s.Failed,
				Duration: latencyCellList(s.Duration),
				Requests: q.Count,
				Status:   fmtStatus(q.Status),
				Errors:   fmtErrors(q.Errors),
				P50:      fmtDur(q.Latency.P50),
				P95:      fmtDur(q.Latency.P95),
				P99:      fmtDur(q.Latency.P99),
				TTFBP50:  fmtDur(q.TTFB.P50),
				TTFBP95:  fmtDur(q.TTFB.P95),
			})
		}
		v.Scenarios = append(v.Scenarios, hs)
	}
	return v
}

// LatencyHeaders は latencyCellList の列名。テンプレートから使う。
func (htmlView) LatencyHeaders() []string { return latencyHeaders }

func latencyCellList(l Latency) []string {
	ds := []time.Duration{l.Min, l.Mean, l.P50, l.P90, l.P95, l.P99, l.Max}
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = fmtDur(d)
	}
	return out
}

// latencyBars は p50〜max を、max に対する割合つきで返す。件数が無ければ nil。
func latencyBars(l Latency) []htmlBar {
	if l.Count == 0 || l.Max <= 0 {
		return nil
	}
	pts := []struct {
		label string
		d     time.Duration
	}{{"p50", l.P50}, {"p90", l.P90}, {"p95", l.P95}, {"p99", l.P99}, {"max", l.Max}}
	bars := make([]htmlBar, len(pts))
	for i, p := range pts {
		bars[i] = htmlBar{Label: p.label, Value: fmtDur(p.d), Pct: float64(p.d) / float64(l.Max) * 100}
	}
	return bars
}

func sortedCounts(m map[string]int) []htmlCount {
	out := make([]htmlCount, 0, len(m))
	for k, n := range m {
		out = append(out, htmlCount{Name: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
