package mcpserver

import (
	"context"

	"github.com/RIRUa/jotaro/report"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addTools registers check_scenario and validate_threshold.
func addTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "check_scenario",
		Description: "Check a jotaro scenario file without running it: Go syntax, jotaro-specific lint rules " +
			"(Plan load model, unchecked responses, duplicate step names) and a type check with go build. " +
			"Pass the whole file (package main). Fix every error and call again until ok is true.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, checkScenario)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "validate_threshold",
		Description: "Parse a threshold expression such as 'p95<200ms' or 'p99{step=login}<100ms' and return its parts, or the parse error.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, validateThreshold)
}

// ThresholdInput is the input of validate_threshold.
type ThresholdInput struct {
	Expression string `json:"expression" jsonschema:"threshold expression, e.g. p95<200ms"`
}

// ThresholdOutput is the output of validate_threshold.
type ThresholdOutput struct {
	Valid bool `json:"valid"`
	// Error is the parse error, set when Valid is false.
	Error string `json:"error,omitempty"`
	// Canonical is the normalized form of the expression.
	Canonical string  `json:"canonical,omitempty"`
	Metric    string  `json:"metric,omitempty"`
	Scenario  string  `json:"scenario,omitempty"`
	Step      string  `json:"step,omitempty"`
	Op        string  `json:"op,omitempty"`
	Value     float64 `json:"value,omitempty"`
}

// validateThreshold parses the expression with report.ParseThreshold.
func validateThreshold(_ context.Context, _ *mcp.CallToolRequest, in ThresholdInput) (*mcp.CallToolResult, ThresholdOutput, error) {
	th, err := report.ParseThreshold(in.Expression)
	if err != nil {
		return nil, ThresholdOutput{Error: err.Error()}, nil
	}
	return nil, ThresholdOutput{
		Valid:     true,
		Canonical: th.String(),
		Metric:    th.Metric,
		Scenario:  th.Scenario,
		Step:      th.Step,
		Op:        th.Op,
		Value:     th.Value,
	}, nil
}
