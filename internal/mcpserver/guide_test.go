package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPromptWorkflow pins that the write_scenario prompt ends with running the scenario and reading its result.
func TestPromptWorkflow(t *testing.T) {
	cs := connect(t)
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "write_scenario", Arguments: map[string]string{"flow": "list items"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"go run .", "results/result.json", "summarize_result", "check_result", "UserStages"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, text)
		}
	}
}

// guideSkeleton returns the first Go code block of the embedded guide, the skeleton a model is told to start from.
func guideSkeleton(t *testing.T) string {
	t.Helper()
	b, err := content.ReadFile("content/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may have CRLF line ends in the embedded file.
	_, rest, ok := strings.Cut(strings.ReplaceAll(string(b), "\r\n", "\n"), "```go\n")
	if !ok {
		t.Fatal("no Go block in the guide")
	}
	code, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("unterminated Go block in the guide")
	}
	return code + "\n"
}

// TestGuideSkeleton pins that the skeleton in the guide compiles and passes check_scenario cleanly, and that
// it does what the rest of the server's text promises: it saves the result and handles an aborted run.
func TestGuideSkeleton(t *testing.T) {
	code := guideSkeleton(t)
	for _, want := range []string{`"results/result.json"`, "os.MkdirAll", "report.FormatJSON", "jotaro.ErrAborted", "errors.Is"} {
		if !strings.Contains(code, want) {
			t.Errorf("skeleton lacks %q", want)
		}
	}
	cs := connect(t)
	var out CheckOutput
	callTool(t, cs, "check_scenario", map[string]any{"code": code, "dir": repoRoot}, &out)
	if !out.OK || !out.TypeChecked || len(out.Diagnostics) != 0 {
		t.Errorf("the guide's skeleton should pass check_scenario cleanly, got %+v", out)
	}
}

// TestGuideCoversAPI pins that the guide mentions the parts of the API it must teach.
func TestGuideCoversAPI(t *testing.T) {
	b, err := content.ReadFile("content/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	guide := string(b)
	for _, want := range []string{
		"Scenarios", "Weighted", "ExpectStatusIn", "ExpectHeader", "ExpectBodyContains", "ExpectJSON", "resp.JSON(path)",
		"UserStages", "Where the files go", "loadtest/", "thresholds.go", "results/", "testdata/",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide does not mention %q", want)
		}
	}
}

// TestDocsMatchErrorRate pins the agreed wording about error_rate: it counts transport-level errors only.
func TestDocsMatchErrorRate(t *testing.T) {
	for _, file := range []string{"content/result.md", "content/thresholds.md"} {
		b, err := content.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, want := range []string{"transport-level error", "iteration_fail_rate"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not mention %q", file, want)
			}
		}
		if strings.Contains(text, "ExpectStatus` mismatches") {
			t.Errorf("%s still says error_rate counts Expect mismatches", file)
		}
	}
	b, _ := content.ReadFile("content/thresholds.md")
	if !strings.Contains(string(b), "dropped<1") {
		t.Error("thresholds.md does not tell to pair corrected_* with dropped<1")
	}
}

// TestResultGuideSeparatesDroppedFromCorrectedGap pins that the checklist does not read a corrected-versus-latency gap as
// a target that could not keep up: in the open model that shows as dropped arrivals, never as delayed ones (#190).
func TestResultGuideSeparatesDroppedFromCorrectedGap(t *testing.T) {
	b, err := content.ReadFile("content/result.md")
	if err != nil {
		t.Fatal(err)
	}
	var item string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "5. **Open model:") {
			item = line
		}
	}
	if item == "" {
		t.Fatal("result.md has no checklist item 5 about the open model")
	}
	for _, want := range []string{"never delayed", "`dropped > 0`", "not from a slow target"} {
		if !strings.Contains(item, want) {
			t.Errorf("checklist item 5 does not say %q", want)
		}
	}
}
