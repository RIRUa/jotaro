package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect starts the server and returns a client session wired to it in memory.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := newServer("v1.2.3").Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callToolError calls a tool that must fail with isError and returns the error text.
func callToolError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("%s: want isError, got %+v", name, res)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if text.Len() == 0 {
		t.Fatalf("%s: isError without a message", name)
	}
	return text.String()
}

// callTool calls a tool and decodes its structured result into out.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error: %+v", res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
}

func TestServerVersion(t *testing.T) {
	cs := connect(t)
	if got := cs.InitializeResult().ServerInfo.Version; got != "v1.2.3" {
		t.Errorf("serverInfo.version = %q, want the version given to newServer", got)
	}
}

func TestResources(t *testing.T) {
	cs := connect(t)
	for _, tt := range []struct{ uri, contains string }{
		{"jotaro://guide", "exactly one"},
		{"jotaro://thresholds", "error_rate"},
		{"jotaro://result", "expect_mismatch"},
	} {
		res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: tt.uri})
		if err != nil {
			t.Fatalf("%s: %v", tt.uri, err)
		}
		if got := res.Contents[0].Text; !strings.Contains(got, tt.contains) {
			t.Errorf("%s: want text containing %q", tt.uri, tt.contains)
		}
	}
}

func TestPrompt(t *testing.T) {
	cs := connect(t)
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "write_scenario",
		Arguments: map[string]string{"flow": "log in and order", "dir": "/work/app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"log in and order", "http://localhost:8080", `dir="/work/app"`} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, text)
		}
	}
}

func TestValidateThreshold(t *testing.T) {
	cs := connect(t)

	var ok ThresholdOutput
	callTool(t, cs, "validate_threshold", map[string]any{"expression": "p99{step=login}<100ms"}, &ok)
	if !ok.Valid || ok.Metric != "p99" || ok.Step != "login" || ok.Op != "<" {
		t.Errorf("valid expression parsed as %+v", ok)
	}

	// An invalid expression is a normal answer (valid: false), not a tool error.
	var bad ThresholdOutput
	callTool(t, cs, "validate_threshold", map[string]any{"expression": "p95~200ms"}, &bad)
	if bad.Valid || bad.Error == "" {
		t.Errorf("invalid expression parsed as %+v", bad)
	}
	if strings.HasPrefix(bad.Error, "jotaro:") {
		t.Errorf("error %q keeps the jotaro: prefix", bad.Error)
	}

	// A value that can never be meant (#82) is invalid, and the error says why.
	for expr, want := range map[string]string{
		"error_rate<500%": "above 100%",
		"p95<-1s":         "negative",
		"rps>NaN":         "NaN",
		"rps>Inf":         "infinite",
	} {
		var out ThresholdOutput
		callTool(t, cs, "validate_threshold", map[string]any{"expression": expr}, &out)
		if out.Valid || !strings.Contains(out.Error, want) || !strings.Contains(out.Error, expr) {
			t.Errorf("%q: parsed as %+v, want an invalid result whose error names the expression and %q", expr, out, want)
		}
	}
}

const goodScenario = `package main

import (
	"context"
	"os"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

var sc = jotaro.NewScenario("s", func(ctx context.Context, u *jotaro.User) error {
	return u.Step("list", func(s *jotaro.Step) error {
		return s.Get("/items").Do().ExpectStatus(200)
	})
})

func main() {
	rep, err := jotaro.Run(context.Background(), jotaro.Plan{Scenario: sc, BaseURL: "http://localhost:1", Users: 1, Duration: time.Second})
	if err != nil {
		panic(err)
	}
	rep.Write(os.Stdout, report.FormatText)
}
`

// repoRoot is the jotaro module, which provides the jotaro package without any network access.
const repoRoot = "../.."

func TestCheckScenario(t *testing.T) {
	cs := connect(t)

	t.Run("ok", func(t *testing.T) {
		var out CheckOutput
		callTool(t, cs, "check_scenario", map[string]any{"code": goodScenario, "dir": repoRoot}, &out)
		if !out.OK || !out.TypeChecked || len(out.Diagnostics) != 0 {
			t.Errorf("want a clean pass, got %+v", out)
		}
	})

	t.Run("type error", func(t *testing.T) {
		code := strings.Replace(goodScenario, `ExpectStatus(200)`, `ExpectStatus("200")`, 1)
		var out CheckOutput
		callTool(t, cs, "check_scenario", map[string]any{"code": code, "dir": repoRoot}, &out)
		if out.OK || !out.TypeChecked {
			t.Fatalf("want a type error, got %+v", out)
		}
		d := out.Diagnostics[0]
		if d.Source != "build" || d.Line == 0 || !strings.Contains(d.Message, "200") {
			t.Errorf("unexpected diagnostic %+v", d)
		}
	})

	t.Run("lint error", func(t *testing.T) {
		code := strings.Replace(goodScenario, "Users: 1,", "Users: 1, Arrival: jotaro.Rate(1, time.Second),", 1)
		var out CheckOutput
		callTool(t, cs, "check_scenario", map[string]any{"code": code, "dir": repoRoot}, &out)
		if out.OK || out.Diagnostics[0].Rule != "plan-load-model" {
			t.Errorf("want plan-load-model, got %+v", out)
		}
	})

	t.Run("syntax error skips the type check", func(t *testing.T) {
		var out CheckOutput
		callTool(t, cs, "check_scenario", map[string]any{"code": "package main\nfunc {"}, &out)
		if out.OK || out.TypeChecked || out.Note == "" {
			t.Errorf("want skipped type check, got %+v", out)
		}
	})

	t.Run("dir without go.mod", func(t *testing.T) {
		var out CheckOutput
		callTool(t, cs, "check_scenario", map[string]any{"code": goodScenario, "dir": t.TempDir()}, &out)
		if out.OK || out.TypeChecked || !strings.Contains(out.Note, "go.mod") {
			t.Errorf("want a note about go.mod, got %+v", out)
		}
	})
}

func TestModuleQuery(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"v0.1.3", "github.com/RIRUa/jotaro@v0.1.3"},
		{"v1.2.3-rc.1", "github.com/RIRUa/jotaro@v1.2.3-rc.1"},
		{"dev", "github.com/RIRUa/jotaro@latest"},
		{"", "github.com/RIRUa/jotaro@latest"},
		{"v0.1.3+dirty", "github.com/RIRUa/jotaro@latest"},
		{"v0.1.4-0.20260102030405-0123456789ab", "github.com/RIRUa/jotaro@latest"},
		{"v0.1.4-0.20260102030405-0123456789ab+dirty", "github.com/RIRUa/jotaro@latest"},
		{"v0.1.3-20260102030405-0123456789ab", "github.com/RIRUa/jotaro@latest"},
		{"0.1.3", "github.com/RIRUa/jotaro@latest"},
		{"(devel)", "github.com/RIRUa/jotaro@latest"},
	} {
		if got := moduleQuery(tc.version); got != tc.want {
			t.Errorf("moduleQuery(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}
