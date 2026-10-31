package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addPrompts registers the write_scenario prompt, which walks the model through writing and checking a scenario.
func addPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "write_scenario",
		Description: "Write a jotaro load-test scenario for a described user flow, then check it.",
		Arguments: []*mcp.PromptArgument{
			{Name: "flow", Description: "The user flow to test, e.g. 'log in, list items, order one'.", Required: true},
			{Name: "base_url", Description: "Base URL of the server under test, e.g. http://localhost:8080."},
			{Name: "dir", Description: "Directory of a Go module that depends on jotaro, used to type-check against its jotaro version."},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		args := req.Params.Arguments
		baseURL := args["base_url"]
		if baseURL == "" {
			baseURL = "http://localhost:8080"
		}
		text := fmt.Sprintf(`Write a jotaro load-test scenario for this flow: %s

1. Read the jotaro://guide resource (and jotaro://thresholds if you will add thresholds).
2. Write one complete package main file: a Scenario with a u.Step per meaningful request, a Plan against %s, and report output.
3. Call check_scenario with the code%s and fix every error and warning it reports. Repeat until it passes.
4. Show the final code, and say which load model you chose (Users or Arrival) and why.

Only target servers the user owns or is allowed to load-test.`,
			args["flow"], baseURL, dirHint(args["dir"]))
		return &mcp.GetPromptResult{
			Description: "Write and check a jotaro scenario",
			Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
		}, nil
	})
}

// dirHint returns the extra instruction for passing dir to check_scenario, or "" when dir is empty.
func dirHint(dir string) string {
	if dir == "" {
		return ""
	}
	return fmt.Sprintf(" and dir=%q", dir)
}
