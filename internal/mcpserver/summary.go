package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loadgenCPUWarn is the CPU usage at or above which the load generator may be saturated.
// It matches the level at which the text and HTML reports show their warning.
const loadgenCPUWarn = 0.9

// SummaryInput is the input of summarize_result.
type SummaryInput struct {
	Path string `json:"path" jsonschema:"path of a result file saved by jotaro (JSON written with report.FormatJSON). Give an absolute path: a relative path is resolved against the working directory of the MCP server, not of your project"`
	Raw  bool   `json:"raw,omitempty" jsonschema:"set to true when path is raw JSON Lines data (the output of Plan.Raw) instead of a JSON result"`
}

// SummaryOutput is the output of summarize_result: the numbers needed to judge a run, without
// the histograms and the per-second series that make the result file large.
type SummaryOutput struct {
	// Model is "open" (Arrival) or "closed" (Users or UserStages).
	Model          string  `json:"model,omitempty"`
	ElapsedSeconds float64 `json:"elapsedSeconds"`
	RPS            float64 `json:"rps"`
	// Dropped is the number of arrivals not run because MaxUsers was reached (open model only).
	Dropped int `json:"dropped"`
	// PeakConcurrency is the largest number of iterations running at once. 0 when the result has no time series.
	PeakConcurrency int               `json:"peakConcurrency"`
	Requests        RequestSummary    `json:"requests"`
	Scenarios       []ScenarioSummary `json:"scenarios"`
	// WorstSecond is the one-second interval with the highest p95. nil when the result has no time series.
	WorstSecond *SecondSummary `json:"worstSecond,omitempty"`
	// LoadGenerator is the health of the machine that generated the load. nil when it was not recorded.
	LoadGenerator *LoadGeneratorSummary `json:"loadGenerator,omitempty"`
	// Warnings lists reasons to distrust or re-run the test, in plain words.
	Warnings []string `json:"warnings"`
}

// RequestSummary is the short form of report.RequestStats.
type RequestSummary struct {
	Count int `json:"count"`
	// ErrorRate is the share of requests that ended in a transport-level error (0 to 1), the same as the error_rate threshold metric.
	// A failed Expect* check is not a request error: it fails the step, and shows in the step Failed counts.
	ErrorRate float64 `json:"errorRate"`
	// Status counts responses per class such as "2xx". HTTP 4xx/5xx are not request errors.
	Status map[string]int `json:"status"`
	// Errors counts request failures per kind (timeout, connection_refused, dns, ...). Failed Expect* checks are not in it.
	Errors  map[string]int `json:"errors"`
	Latency LatencySummary `json:"latency"`
	// Corrected is the latency from the scheduled start (Coordinated Omission correction). Open model only.
	Corrected *LatencySummary `json:"corrected,omitempty"`
}

// LatencySummary is a few points of a latency distribution, in milliseconds.
type LatencySummary struct {
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	P99Ms float64 `json:"p99Ms"`
	MaxMs float64 `json:"maxMs"`
}

// ScenarioSummary is the short form of report.ScenarioReport.
type ScenarioSummary struct {
	Name             string        `json:"name"`
	Iterations       int           `json:"iterations"`
	FailedIterations int           `json:"failedIterations"`
	Steps            []StepSummary `json:"steps"`
}

// StepSummary is the short form of report.StepStats.
type StepSummary struct {
	Name string `json:"name"`
	// Count and Failed count runs of the step; Requests covers the HTTP requests sent inside it.
	Count    int            `json:"count"`
	Failed   int            `json:"failed"`
	Requests RequestSummary `json:"requests"`
}

// SecondSummary is one point of the per-second series.
type SecondSummary struct {
	// Second is the seconds elapsed since the start of the run (0-based).
	Second   int     `json:"second"`
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	P95Ms    float64 `json:"p95Ms"`
}

// LoadGeneratorSummary is the health of the load generator over the run.
type LoadGeneratorSummary struct {
	// MaxCPU is the highest CPU usage of the process (0 to 1). nil on platforms where it cannot be measured.
	MaxCPU        *float64 `json:"maxCpu,omitempty"`
	MaxGoroutines int      `json:"maxGoroutines"`
}

// summarizeResult reads a saved result and returns its key numbers.
func summarizeResult(_ context.Context, _ *mcp.CallToolRequest, in SummaryInput) (*mcp.CallToolResult, SummaryOutput, error) {
	rep, err := loadReport(in.Path, in.Raw)
	if err != nil {
		// A file that cannot be read is a tool error (isError), not a summary of zeros.
		return nil, SummaryOutput{}, errors.New(trimPrefix(err))
	}
	return nil, summarize(rep), nil
}

// summarize builds the summary of rep.
func summarize(rep *report.Report) SummaryOutput {
	out := SummaryOutput{
		Model:          "closed",
		ElapsedSeconds: round3(rep.Elapsed.Seconds()),
		RPS:            round3(rep.RPS()),
		Dropped:        rep.Dropped,
		Requests:       summarizeRequests(rep.Requests, rep.Open),
		Scenarios:      make([]ScenarioSummary, 0, len(rep.Scenarios)),
		Warnings:       []string{},
	}
	if rep.Open {
		out.Model = "open"
	}
	for _, sc := range rep.Scenarios {
		s := ScenarioSummary{
			Name:             sc.Name,
			Iterations:       sc.Iterations.Count,
			FailedIterations: sc.Iterations.Failed,
			Steps:            make([]StepSummary, 0, len(sc.Steps)),
		}
		for _, st := range sc.Steps {
			s.Steps = append(s.Steps, StepSummary{
				Name:     st.Name,
				Count:    st.Count,
				Failed:   st.Failed,
				Requests: summarizeRequests(st.Requests, rep.Open),
			})
		}
		out.Scenarios = append(out.Scenarios, s)
	}

	for _, p := range rep.Series {
		out.PeakConcurrency = max(out.PeakConcurrency, p.Concurrency)
		if p.Latency.Count == 0 {
			continue
		}
		if p95 := ms(p.Latency.P95); out.WorstSecond == nil || p95 > out.WorstSecond.P95Ms {
			out.WorstSecond = &SecondSummary{Second: p.Second, Requests: p.Requests, Errors: p.Errors, P95Ms: p95}
		}
	}
	out.LoadGenerator = summarizeLoadGenerator(rep.Health)

	if rep.Requests.Count == 0 {
		out.Warnings = append(out.Warnings, "no requests were recorded: the scenario may not have run, or it sent requests through u.Client, which is not measured")
	}
	if rep.Dropped > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"%d arrivals were dropped because MaxUsers was reached: the target could not keep up with the arrival rate, and the measured load is lower than planned", rep.Dropped))
	}
	if lg := out.LoadGenerator; lg != nil && lg.MaxCPU != nil && *lg.MaxCPU >= loadgenCPUWarn {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"load generator CPU reached %.0f%%: measured latencies may be worse than the target's actual behavior; lower the load or use more load generators", *lg.MaxCPU*100))
	}
	return out
}

// summarizeRequests shortens q. Corrected is filled only for the open model, where it differs from Latency.
func summarizeRequests(q report.RequestStats, open bool) RequestSummary {
	s := RequestSummary{
		Count:   q.Count,
		Status:  map[string]int{},
		Errors:  map[string]int{},
		Latency: summarizeLatency(q.Latency),
	}
	for class, n := range q.Status {
		s.Status[class] = n
	}
	failed := 0
	for kind, n := range q.Errors {
		s.Errors[string(kind)] = n
		failed += n
	}
	if q.Count > 0 {
		s.ErrorRate = float64(failed) / float64(q.Count)
	}
	if open {
		c := summarizeLatency(q.Corrected)
		s.Corrected = &c
	}
	return s
}

func summarizeLatency(l report.Latency) LatencySummary {
	return LatencySummary{P50Ms: ms(l.P50), P95Ms: ms(l.P95), P99Ms: ms(l.P99), MaxMs: ms(l.Max)}
}

// summarizeLoadGenerator returns nil when there are no health points.
func summarizeLoadGenerator(pts []record.HealthPoint) *LoadGeneratorSummary {
	if len(pts) == 0 {
		return nil
	}
	lg := &LoadGeneratorSummary{}
	for _, p := range pts {
		lg.MaxGoroutines = max(lg.MaxGoroutines, p.Goroutines)
		if p.CPU < 0 { // not measurable on this platform
			continue
		}
		if lg.MaxCPU == nil || p.CPU > *lg.MaxCPU {
			cpu := p.CPU
			lg.MaxCPU = &cpu
		}
	}
	return lg
}

// ms converts d to milliseconds with microsecond precision.
func ms(d time.Duration) float64 {
	return round3(float64(d) / float64(time.Millisecond))
}

// round3 rounds v to three decimal places, which keeps the output short without losing anything a reader would use.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
