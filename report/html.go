package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"
)

// reportHTMLSrc is the template of the whole page. CSS is inline too, so the output needs no other file.
//
//go:embed report.html.tmpl
var reportHTMLSrc string

// reportHTML is parsed once at start-up. Template.Must makes a broken template fail on import, not at report time.
var reportHTML = template.Must(template.New("report").Parse(reportHTMLSrc))

// Types for display. Strings and percentages are prepared up front so the template does no computation.

// htmlBar is one horizontal bar of a latency chart.
type htmlBar struct {
	Label string
	Value string
	Pct   float64 // 0 to 100; the share relative to the maximum
}

// htmlCount is a label with a count, such as a status class ("2xx") or an error kind.
type htmlCount struct {
	Name  string
	Count int
}

// htmlLatencyRow is one row of a latency table, with columns as in latencyHeaders.
type htmlLatencyRow struct {
	Label string
	Cells []string // min, mean, p50, p90, p95, p99, max
}

// htmlStep is one row of the per-step table, already formatted as text.
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

// htmlScenario is the view of one ScenarioReport.
type htmlScenario struct {
	Name       string
	Iterations int
	PerSecond  string
	Failed     int
	Errors     string
	Latency    []htmlLatencyRow
	Steps      []htmlStep
}

// htmlView is everything the template needs. Fields that may be absent are empty (nil or ""),
// and the template skips their sections.
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
	Latency   []htmlBar // latency distribution of requests
	Corrected []htmlBar // only when Open
	TTFB      []htmlBar
	Conns     string // only when connection records exist
	LoadGen   string // health of the load generator; only when records exist
	LoadWarn  string // note shown when the load generator may be saturated
	DNS       []htmlBar
	Connect   []htmlBar
	TLS       []htmlBar
	Charts    []htmlChart // time series; nil if there is none
	Scenarios []htmlScenario
}

// latencyHeaders are the column names that match the cells from latencyCellList.
var latencyHeaders = []string{"min", "mean", "p50", "p90", "p95", "p99", "max"}

// WriteHTML writes the report to w as a single self-contained HTML file.
func (r *Report) WriteHTML(w io.Writer) error {
	return reportHTML.Execute(w, r.toHTMLView())
}

// toHTMLView converts the report to its display form: durations and sizes become strings,
// and bar lengths become percentages, so the template does no arithmetic.
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
		Charts:   seriesCharts(r.Series),
		DNS:      latencyBars(r.Requests.DNS),
		Connect:  latencyBars(r.Requests.Connect),
		TLS:      latencyBars(r.Requests.TLS),
	}
	if q := r.Requests; q.NewConns+q.ReusedConns > 0 {
		v.Conns = fmt.Sprintf("%.1f%% reused (new %d / reused %d)", q.ReuseRate()*100, q.NewConns, q.ReusedConns)
	}
	if h, ok := summarizeHealth(r.Health); ok {
		v.LoadGen = h.String()
		if h.Hot() {
			v.LoadWarn = h.warning()
		}
	}
	v.Charts = append(v.Charts, healthCharts(r.Health)...)
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

// LatencyHeaders are the column names of latencyCellList, for use from the template.
func (htmlView) LatencyHeaders() []string { return latencyHeaders }

// latencyCellList formats min, mean, p50, p90, p95, p99 and max of l, in that order.
func latencyCellList(l Latency) []string {
	ds := []time.Duration{l.Min, l.Mean, l.P50, l.P90, l.P95, l.P99, l.Max}
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = fmtDur(d)
	}
	return out
}

// latencyBars returns p50 to max with each as a share of max. It returns nil when there are no observations.
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

// sortedCounts turns a tally into a list sorted by name, so the page does not change between runs.
func sortedCounts(m map[string]int) []htmlCount {
	out := make([]htmlCount, 0, len(m))
	for k, n := range m {
		out = append(out, htmlCount{Name: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
