package mcpserver

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

const ms1 = time.Millisecond

// lat is a latency distribution whose percentiles all are p, which is all the warnings read.
func lat(count int, p time.Duration) report.Latency {
	return report.Latency{Count: count, P50: p, P95: p, P99: p, Max: p}
}

// healthyReport is a closed-model run of 60 s that must produce no warning: two steps with hundreds of requests, only
// 2xx, no failed step, steady concurrency and a flat latency series.
func healthyReport() *report.Report {
	step := func(name string, n int) report.StepStats {
		return report.StepStats{
			Name: name, Count: n,
			Requests: report.RequestStats{
				Count: n, Latency: lat(n, 20*ms1), Corrected: lat(n, 20*ms1),
				Status: map[string]int{"2xx": n}, Errors: map[record.ErrKind]int{},
			},
		}
	}
	rep := &report.Report{
		Elapsed: 60 * time.Second,
		Requests: report.RequestStats{
			Count: 800, Latency: lat(800, 20*ms1), Corrected: lat(800, 20*ms1),
			Status: map[string]int{"2xx": 800}, Errors: map[record.ErrKind]int{},
		},
		Scenarios: []report.ScenarioReport{{
			Name:       "shop",
			Iterations: report.IterationStats{Count: 500},
			Steps:      []report.StepStats{step("browse", 500), step("buy", 300)},
		}},
	}
	rep.Series = series(60, func(int) report.SeriesPoint {
		return report.SeriesPoint{Requests: 13, Concurrency: 10, Latency: lat(13, 20*ms1)}
	})
	return rep
}

func series(n int, f func(second int) report.SeriesPoint) []report.SeriesPoint {
	pts := make([]report.SeriesPoint, n)
	for i := range pts {
		pts[i] = f(i)
		pts[i].Second = i
	}
	return pts
}

// withCount sets the request count of the run, and of the browse and buy steps.
func withCount(rep *report.Report, total, browse, buy int) {
	rep.Requests.Count = total
	rep.Requests.Status = map[string]int{"2xx": total}
	steps := rep.Scenarios[0].Steps
	steps[0].Requests.Count, steps[0].Requests.Status = browse, map[string]int{"2xx": browse}
	steps[1].Requests.Count, steps[1].Requests.Status = buy, map[string]int{"2xx": buy}
}

// slowFailures is a 60 s series with 1 request per second in which the last third (20 requests) has n 5xx.
func slowFailures(n int) []report.SeriesPoint {
	return series(60, func(i int) report.SeriesPoint {
		p := report.SeriesPoint{Requests: 1, Concurrency: 1, Latency: lat(1, 20*ms1)}
		if i >= 60-n {
			p.HTTP5xx = 1
		}
		return p
	})
}

func codes(ws []warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

// TestReviewWarnings checks, for every warning, a result that must produce it and results that must not (the
// boundaries and the healthy look-alikes), and that the healthy report produces none at all.
func TestReviewWarnings(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*report.Report)
		want   []string // exact set of codes
		in     string   // a part that the message of the first warning must contain
	}{
		"healthy run": {mutate: func(*report.Report) {}},

		// few_samples
		"run of 99 requests: p99 is the max": {
			mutate: func(r *report.Report) { withCount(r, 99, 50, 49) },
			want:   []string{warnFewSamples}, in: "only 99 requests were recorded: p99 is the slowest request",
		},
		"steps and run of exactly 100 requests": {mutate: func(r *report.Report) { withCount(r, 200, 100, 100) }},
		"run of 19 requests: p95 is the max too": {
			mutate: func(r *report.Report) { withCount(r, 19, 10, 9) },
			want:   []string{warnFewSamples}, in: "p95 and p99 are the slowest request",
		},
		"run of 20 requests: only p99 is the max": {
			mutate: func(r *report.Report) { withCount(r, 20, 10, 10) },
			want:   []string{warnFewSamples}, in: "p99 is the slowest request",
		},
		"one thin step in a long run": {
			mutate: func(r *report.Report) { withCount(r, 1000, 940, 60) },
			want:   []string{warnFewSamples}, in: "shop/buy (60 requests, p99 is the slowest request)",
		},
		"no requests is another warning's job": {
			mutate: func(r *report.Report) { withCount(r, 0, 0, 0) },
		},

		// failed_checks
		"failed step without any request error": {
			mutate: func(r *report.Report) { r.Scenarios[0].Steps[1].Failed = 90 },
			want:   []string{warnFailedChecks}, in: "step shop/buy failed 90 of 300 runs, at least 90 of them with no request error",
		},
		"failed step explained by timeouts": {
			mutate: func(r *report.Report) {
				st := &r.Scenarios[0].Steps[1]
				st.Failed = 5
				st.Requests.Errors = map[record.ErrKind]int{record.ErrTimeout: 5}
				r.Requests.Errors = map[record.ErrKind]int{record.ErrTimeout: 5}
			},
		},
		"failed step with a few timeouts and many wrong answers": {
			mutate: func(r *report.Report) {
				st := &r.Scenarios[0].Steps[1]
				st.Failed = 50
				st.Requests.Errors = map[record.ErrKind]int{record.ErrTimeout: 5}
				r.Requests.Errors = map[record.ErrKind]int{record.ErrTimeout: 5}
			},
			want: []string{warnFailedChecks}, in: "at least 45 of them",
		},
		"scenario returned its own error": {
			mutate: func(r *report.Report) { r.Scenarios[0].Iterations.Failed = 40 },
			want:   []string{warnFailedChecks}, in: "scenario shop failed 40 of 500 iterations",
		},
		"failed iterations with request errors present": {
			mutate: func(r *report.Report) {
				r.Scenarios[0].Iterations.Failed = 3
				r.Requests.Errors = map[record.ErrKind]int{record.ErrTimeout: 3}
			},
		},

		// client_errors
		"a tenth of a step is 4xx": {
			mutate: func(r *report.Report) {
				r.Scenarios[0].Steps[0].Failed = 50
				r.Scenarios[0].Steps[0].Requests.Status = map[string]int{"2xx": 450, "4xx": 50}
				r.Requests.Status = map[string]int{"2xx": 750, "4xx": 50}
			},
			want: []string{warnFailedChecks, warnClientErrors}, in: "shop/browse 10% (50 of 500)",
		},
		"a tenth of a step is 4xx but no run failed": {
			mutate: func(r *report.Report) {
				r.Scenarios[0].Steps[0].Failed = 0
				r.Scenarios[0].Steps[0].Requests.Status = map[string]int{"2xx": 450, "4xx": 50}
				r.Requests.Status = map[string]int{"2xx": 750, "4xx": 50}
			},
		},
		"step that expects the rejection": {
			// A negative check (ExpectStatus(404), a wrong password answered with 401): all 4xx, nothing failed.
			mutate: func(r *report.Report) {
				r.Scenarios[0].Steps[1].Requests.Status = map[string]int{"4xx": 300}
				r.Requests.Status = map[string]int{"2xx": 500, "4xx": 300}
			},
		},
		"under a tenth is 4xx": {
			mutate: func(r *report.Report) {
				r.Scenarios[0].Steps[0].Requests.Status = map[string]int{"2xx": 451, "4xx": 49}
				r.Requests.Status = map[string]int{"2xx": 751, "4xx": 49}
			},
		},
		"few 4xx responses decide nothing": {
			// 9 of a tiny step is a large share, but nine responses are below the minimum count.
			mutate: func(r *report.Report) {
				r.Scenarios[0].Steps[1].Requests = report.RequestStats{
					Count: 100, Latency: lat(100, 20*ms1), Status: map[string]int{"2xx": 91, "4xx": 9},
				}
				r.Scenarios[0].Steps[1].Count = 100
				r.Scenarios[0].Steps[1].Failed = 9
				r.Requests.Status = map[string]int{"2xx": 791, "4xx": 9}
			},
			want: []string{warnFailedChecks}, // the failed runs are reported, the nine 4xx are not
		},
		"throttled run": {
			mutate: func(r *report.Report) {
				r.Requests.Status = map[string]int{"2xx": 400, "4xx": 400}
				for i := range r.Scenarios[0].Steps {
					st := &r.Scenarios[0].Steps[i]
					st.Failed = st.Count / 2 // the checks that expected a success failed
					st.Requests.Status = map[string]int{"2xx": st.Count / 2, "4xx": st.Count / 2}
				}
			},
			want: []string{warnFailedChecks, warnClientErrors}, in: "rate limiter",
		},

		// late_arrivals
		"open model with corrected far above latency": {
			mutate: func(r *report.Report) { r.Open = true; r.Requests.Corrected = lat(800, 50*ms1) },
			want:   []string{warnLateArrivals}, in: "corrected p95 is 50ms against a latency p95 of 20ms",
		},
		"open model, corrected exactly twice latency": {
			mutate: func(r *report.Report) { r.Open = true; r.Requests.Corrected = lat(800, 40*ms1) },
			want:   []string{warnLateArrivals},
		},
		"open model, corrected a little above latency": {
			mutate: func(r *report.Report) { r.Open = true; r.Requests.Corrected = lat(800, 39*ms1) },
		},
		"open model, large ratio but a few milliseconds": {
			mutate: func(r *report.Report) {
				r.Open = true
				r.Requests.Latency, r.Requests.Corrected = lat(800, 1*ms1), lat(800, 8*ms1)
			},
		},
		"closed model ignores corrected": {
			mutate: func(r *report.Report) { r.Requests.Corrected = lat(800, 500*ms1) },
		},

		// unstable_run
		"latency triples over a steady load": {
			mutate: func(r *report.Report) {
				r.Series = series(60, func(i int) report.SeriesPoint {
					p := 20 * ms1
					if i >= 40 {
						p = 60 * ms1
					}
					return report.SeriesPoint{Requests: 13, Concurrency: 10, Latency: lat(13, p)}
				})
			},
			want: []string{warnUnstableRun}, in: "rose from 20ms in the first third of the run to 60ms in the last third",
		},
		"latency rises a little": {
			mutate: func(r *report.Report) {
				r.Series = series(60, func(i int) report.SeriesPoint {
					p := 20 * ms1
					if i >= 40 {
						p = 35 * ms1
					}
					return report.SeriesPoint{Requests: 13, Concurrency: 10, Latency: lat(13, p)}
				})
			},
		},
		"latency doubles but only by milliseconds": {
			mutate: func(r *report.Report) {
				r.Series = series(60, func(i int) report.SeriesPoint {
					p := 2 * ms1
					if i >= 40 {
						p = 6 * ms1
					}
					return report.SeriesPoint{Requests: 13, Concurrency: 10, Latency: lat(13, p)}
				})
			},
		},
		"two slow seconds": {
			mutate: func(r *report.Report) {
				r.Series[55].Latency = lat(13, 900*ms1)
				r.Series[56].Latency = lat(13, 900*ms1)
			},
		},
		"deliberate ramp of users": {
			mutate: func(r *report.Report) {
				r.Series = series(60, func(i int) report.SeriesPoint {
					return report.SeriesPoint{Requests: 13, Concurrency: 5 + i, Latency: lat(13, time.Duration(20+3*i)*ms1)}
				})
			},
		},
		"open model with a rising arrival rate": {
			mutate: func(r *report.Report) {
				r.Open = true
				r.Series = series(60, func(i int) report.SeriesPoint {
					return report.SeriesPoint{Requests: 5 + i, Concurrency: 10, Latency: lat(5+i, time.Duration(20+3*i)*ms1)}
				})
			},
		},
		"open model, steady arrivals, latency grows": {
			// In the open model concurrency grows with latency by itself; the arrival rate is the load.
			mutate: func(r *report.Report) {
				r.Open = true
				r.Series = series(60, func(i int) report.SeriesPoint {
					return report.SeriesPoint{Requests: 13, Concurrency: 5 + i, Latency: lat(13, time.Duration(20+3*i)*ms1)}
				})
			},
			want: []string{warnUnstableRun},
		},
		"failures rise over a steady load": {
			mutate: func(r *report.Report) {
				r.Series = series(60, func(i int) report.SeriesPoint {
					p := report.SeriesPoint{Requests: 20, Concurrency: 10, Latency: lat(20, 20*ms1)}
					if i >= 40 {
						p.HTTP5xx = 3
					}
					return p
				})
			},
			want: []string{warnUnstableRun}, in: "rose from 0.0% to 15.0%",
		},
		"one 503 in a slow run": {
			// 1 request per second: one failure is 5% of the last third, and no trend.
			mutate: func(r *report.Report) {
				r.Series = series(60, func(i int) report.SeriesPoint {
					p := report.SeriesPoint{Requests: 1, Concurrency: 1, Latency: lat(1, 20*ms1)}
					if i == 59 {
						p.HTTP5xx = 1
					}
					return p
				})
			},
		},
		"nine failures in the last third": {mutate: func(r *report.Report) { r.Series = slowFailures(9) }},
		"ten failures in the last third": {
			mutate: func(r *report.Report) { r.Series = slowFailures(10) },
			want:   []string{warnUnstableRun}, in: "rose from 0.0% to 50.0%",
		},
		"short run is not cut in thirds": {
			mutate: func(r *report.Report) {
				r.Series = series(20, func(i int) report.SeriesPoint {
					p := 20 * ms1
					if i >= 14 {
						p = 200 * ms1
					}
					return report.SeriesPoint{Requests: 13, Concurrency: 10, Latency: lat(13, p)}
				})
			},
		},
		"no series": {mutate: func(r *report.Report) { r.Series = nil }},
	} {
		t.Run(name, func(t *testing.T) {
			rep := healthyReport()
			tc.mutate(rep)
			ws := reviewWarnings(rep)
			if got := codes(ws); !slices.Equal(got, tc.want) {
				t.Fatalf("warnings = %q, want %q (%+v)", got, tc.want, ws)
			}
			if tc.in != "" && !strings.Contains(ws[len(ws)-1].Message, tc.in) {
				t.Errorf("last message = %q, want it to contain %q", ws[len(ws)-1].Message, tc.in)
			}
		})
	}
}

// TestSummarizeAddsReviewWarnings pins that the warnings reach the output of summarize, after the existing ones.
func TestSummarizeAddsReviewWarnings(t *testing.T) {
	rep := healthyReport()
	if out := summarize(rep); len(out.Warnings) != 0 {
		t.Fatalf("healthy report has warnings: %q", out.Warnings)
	}
	rep.Dropped = 3
	withCount(rep, 50, 25, 25)
	out := summarize(rep)
	if len(out.Warnings) != 2 || !strings.Contains(out.Warnings[0], "dropped") || !strings.Contains(out.Warnings[1], "only 50 requests") {
		t.Errorf("want the dropped warning, then the few-samples one, got %q", out.Warnings)
	}
}
