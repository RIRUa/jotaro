package mcpserver

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loadReport reads a saved result from path. With raw, the file is raw JSON Lines data
// (the output of Plan.Raw) and is aggregated again, as "jotaro check -raw" does.
func loadReport(path string, raw bool) (*report.Report, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only; a close error loses nothing

	if raw {
		r, err := record.ReadRaw(f)
		if err != nil {
			return nil, err
		}
		return report.FromRaw(r), nil
	}
	return report.ReadJSON(f)
}

// CheckResultInput is the input of check_result.
type CheckResultInput struct {
	Path       string   `json:"path" jsonschema:"path of a result file saved by jotaro (JSON written with report.FormatJSON)"`
	Thresholds []string `json:"thresholds" jsonschema:"threshold expressions such as p95<200ms or error_rate<1%; see the jotaro://thresholds resource"`
	Raw        bool     `json:"raw,omitempty" jsonschema:"set to true when path is raw JSON Lines data (the output of Plan.Raw) instead of a JSON result"`
}

// CheckResultOutput is the output of check_result.
type CheckResultOutput struct {
	// Passed is true only when the file was read and every threshold was met.
	Passed bool `json:"passed"`
	// Error is set when nothing could be judged: the file could not be read, or a threshold could not be parsed.
	Error   string            `json:"error,omitempty"`
	Results []ThresholdResult `json:"results"`
	// Text is the same listing "jotaro check" prints, with the values in readable units.
	Text string `json:"text,omitempty"`
}

// ThresholdResult is the outcome of one threshold against one target.
type ThresholdResult struct {
	// Threshold is the normalized expression.
	Threshold string `json:"threshold"`
	// Target is the scenario and step that were looked at, such as "checkout/login". Empty for the whole run.
	Target string `json:"target,omitempty"`
	Pass   bool   `json:"pass"`
	// Actual is the measured value in the unit of the metric: nanoseconds for durations, 0 to 1 for ratios.
	// It is meaningless when Note is set.
	Actual float64 `json:"actual"`
	// Note says why the threshold could not be judged (no data, no matching step, ...). Such a threshold counts as not met.
	Note string `json:"note,omitempty"`
}

// checkResult judges a saved result against thresholds, like "jotaro check".
func checkResult(_ context.Context, _ *mcp.CallToolRequest, in CheckResultInput) (*mcp.CallToolResult, CheckResultOutput, error) {
	fail := func(msg string) (*mcp.CallToolResult, CheckResultOutput, error) {
		return nil, CheckResultOutput{Error: msg, Results: []ThresholdResult{}}, nil
	}
	if len(in.Thresholds) == 0 {
		return fail("no thresholds given")
	}
	// Parse first, so that a typo is reported even when the file is also wrong.
	ths := make([]report.Threshold, 0, len(in.Thresholds))
	for _, s := range in.Thresholds {
		th, err := report.ParseThreshold(s)
		if err != nil {
			return fail(trimPrefix(err))
		}
		ths = append(ths, th)
	}
	rep, err := loadReport(in.Path, in.Raw)
	if err != nil {
		return fail(trimPrefix(err))
	}

	checks := rep.Check(ths...)
	out := CheckResultOutput{Passed: checks.Passed(), Results: make([]ThresholdResult, 0, len(checks))}
	for _, c := range checks {
		out.Results = append(out.Results, ThresholdResult{
			Threshold: c.Threshold.String(),
			Target:    c.Target,
			Pass:      c.Pass,
			Actual:    c.Actual,
			Note:      c.Note,
		})
	}
	var text strings.Builder
	checks.Print(&text)
	out.Text = text.String()
	return nil, out, nil
}

// trimPrefix drops the "jotaro: " prefix the library puts on its errors, which adds nothing inside a tool result.
func trimPrefix(err error) string {
	return strings.TrimPrefix(err.Error(), "jotaro: ")
}
