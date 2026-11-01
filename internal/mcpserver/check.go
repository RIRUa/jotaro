package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/scanner"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// goTimeout bounds each go command, since resolving jotaro in a fresh module can hit the network.
const goTimeout = 2 * time.Minute

// CheckInput is the input of check_scenario.
type CheckInput struct {
	Code string `json:"code" jsonschema:"the complete scenario file (package main)"`
	// Dir is optional.
	Dir string `json:"dir,omitempty" jsonschema:"optional absolute path of a Go module that requires jotaro; the code is type-checked against that module. Without it a temporary module with the latest jotaro is used (needs network)"`
}

// CheckOutput is the output of check_scenario.
type CheckOutput struct {
	// OK is true only when the type check ran and there is no error. Warnings do not make it false.
	OK          bool         `json:"ok"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	// TypeChecked is false when go build could not be run; Note says why.
	TypeChecked bool   `json:"typeChecked"`
	Note        string `json:"note,omitempty"`
}

// checkScenario lints the code and, when it parses, type-checks it by compiling it with go build. Nothing is executed.
func checkScenario(ctx context.Context, _ *mcp.CallToolRequest, in CheckInput) (*mcp.CallToolResult, CheckOutput, error) {
	diags := lintScenario(in.Code)
	out := CheckOutput{Diagnostics: diags}
	if hasSource(diags, "syntax") {
		out.Note = "type check skipped: fix the syntax errors first"
		return nil, finish(out), nil
	}
	buildDiags, err := buildScenario(ctx, in.Code, in.Dir)
	if err != nil {
		out.Note = "type check skipped: " + err.Error()
		return nil, finish(out), nil
	}
	out.TypeChecked = true
	out.Diagnostics = append(out.Diagnostics, buildDiags...)
	return nil, finish(out), nil
}

func finish(out CheckOutput) CheckOutput {
	out.OK = out.TypeChecked
	for _, d := range out.Diagnostics {
		if d.Severity == "error" {
			out.OK = false
		}
	}
	if out.Diagnostics == nil {
		out.Diagnostics = []Diagnostic{}
	}
	return out
}

func hasSource(diags []Diagnostic, source string) bool {
	for _, d := range diags {
		if d.Source == source {
			return true
		}
	}
	return false
}

// syntaxDiagnostics converts a parser error into diagnostics.
func syntaxDiagnostics(err error) []Diagnostic {
	var list scanner.ErrorList
	if !errors.As(err, &list) {
		return []Diagnostic{{Severity: "error", Source: "syntax", Message: err.Error()}}
	}
	diags := make([]Diagnostic, 0, len(list))
	for _, e := range list {
		diags = append(diags, Diagnostic{Severity: "error", Source: "syntax", Line: e.Pos.Line, Column: e.Pos.Column, Message: e.Msg})
	}
	return diags
}

// buildScenario type-checks code with go build and returns what it reports.
//
// With dir, the file is added to that module as a throwaway package through an overlay, so nothing
// is written into it. Without dir, a temporary module that depends on the latest jotaro is used.
func buildScenario(ctx context.Context, code, dir string) ([]Diagnostic, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return nil, errors.New("the go command is not in PATH")
	}
	tmp, err := os.MkdirTemp("", "jotaro-mcp-check-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // best-effort cleanup of a scratch directory

	file := filepath.Join(tmp, "scenario.go")
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	ctx, cancel := context.WithTimeout(ctx, goTimeout)
	defer cancel()
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			return nil, fmt.Errorf("dir %q is not a Go module (no go.mod)", dir)
		}
		// The package directory does not exist in dir; the overlay makes it appear to the go command.
		pkgDir := filepath.Join(abs, "zz_jotaro_mcp_check")
		overlay, err := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(pkgDir, "scenario.go"): file}})
		if err != nil {
			return nil, err
		}
		overlayFile := filepath.Join(tmp, "overlay.json")
		if err := os.WriteFile(overlayFile, overlay, 0o600); err != nil {
			return nil, err
		}
		cmd = exec.CommandContext(ctx, "go", "build", "-o", os.DevNull, "-overlay="+overlayFile, "./zz_jotaro_mcp_check")
		cmd.Dir = abs
	} else {
		if err := os.Remove(file); err != nil {
			return nil, err
		}
		modDir := filepath.Join(tmp, "m")
		if err := os.Mkdir(modDir, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(modDir, "scenario.go"), []byte(code), 0o600); err != nil {
			return nil, err
		}
		for _, args := range [][]string{
			{"mod", "init", "scenariocheck"},
			{"get", "github.com/RIRUa/jotaro@latest"},
		} {
			if out, err := runGo(ctx, modDir, args...); err != nil {
				return nil, fmt.Errorf("go %s: %s", strings.Join(args, " "), firstLines(out, 5))
			}
		}
		cmd = exec.CommandContext(ctx, "go", "build", "-o", os.DevNull, ".")
		cmd.Dir = modDir
	}

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("go build timed out after %s", goTimeout)
	}
	if err == nil {
		return nil, nil
	}
	diags := parseBuildOutput(buf.String())
	if len(diags) == 0 {
		// Not a source-level problem (missing module, network, ...), so say so instead of blaming the code.
		return nil, fmt.Errorf("go build failed: %s", firstLines(buf.String(), 5))
	}
	return diags, nil
}

// runGo runs a go command in dir and returns its combined output.
func runGo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// buildLine matches "path/to/file.go:12:3: message".
var buildLine = regexp.MustCompile(`^(\S+\.go):(\d+):(?:(\d+):)? (.*)$`)

// parseBuildOutput extracts position-bearing lines from go build output.
func parseBuildOutput(out string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(out, "\n") {
		m := buildLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		diags = append(diags, Diagnostic{Severity: "error", Source: "build", Line: ln, Column: col, Message: m[4]})
	}
	return diags
}

// firstLines returns at most n non-empty lines of s.
func firstLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
		if len(lines) == n {
			break
		}
	}
	return strings.Join(lines, "; ")
}
