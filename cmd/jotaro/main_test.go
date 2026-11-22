package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// writeSample writes a small result as JSON to path.
func writeSample(t *testing.T, path string) {
	t.Helper()
	rep := report.BuildReport(
		[]record.Record{{Scenario: "checkout", Step: "login", Code: 200, Latency: 12 * time.Millisecond}},
		[]record.StepRecord{{Scenario: "checkout", Step: "login", Duration: 15 * time.Millisecond}},
		[]record.IterationRecord{{Scenario: "checkout", Duration: 20 * time.Millisecond}},
		time.Second,
	)
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReportToFile(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "result.json"), filepath.Join(dir, "report.html")
	writeSample(t, in)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"report", "-o", out, in}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "<!doctype html>") || !strings.Contains(string(b), "checkout") {
		t.Fatalf("not HTML: %.200s", b)
	}
}

func TestReportStdinToStdout(t *testing.T) {
	in := filepath.Join(t.TempDir(), "result.json")
	writeSample(t, in)
	data, _ := os.ReadFile(in)

	var stdout, stderr bytes.Buffer
	code := run([]string{"report", "-format", "text", "-"}, bytes.NewReader(data), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "=== Summary ===") {
		t.Fatalf("not text: %s", stdout.String())
	}
}

func TestReportErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"version": 999}`), 0o644)

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no arguments", nil, 2},
		{"unknown command", []string{"nope"}, 2},
		{"mcp with arguments", []string{"mcp", "extra"}, 2},
		{"mcp with unknown flag", []string{"mcp", "-x"}, 2},
		{"no input", []string{"report"}, 2},
		{"unknown format", []string{"report", "-format", "csv", bad}, 2},
		{"missing file", []string{"report", filepath.Join(dir, "none.json")}, 1},
		{"unsupported version", []string{"report", bad}, 1},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(tt.args, strings.NewReader(""), &stdout, &stderr); code != tt.want {
			t.Errorf("%s: code = %d, want %d (stderr: %s)", tt.name, code, tt.want, stderr.String())
		}
	}
}

func TestMerge(t *testing.T) {
	dir := t.TempDir()
	a, b, out := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"), filepath.Join(dir, "merged.json")
	writeSample(t, a)
	writeSample(t, b)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"merge", "-o", out, a, b}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rep, err := report.ReadJSON(f)
	if err != nil {
		t.Fatalf("merged result is not readable as JSON: %v", err)
	}
	if rep.Requests.Count != 2 || rep.Scenarios[0].Iterations.Count != 2 {
		t.Fatalf("the two inputs were not summed: %+v", rep.Requests)
	}
}

func TestMergeErrors(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.json")
	writeSample(t, ok)

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no input", []string{"merge"}, 2},
		{"unknown format", []string{"merge", "-format", "csv", ok}, 2},
		{"missing file", []string{"merge", ok, filepath.Join(dir, "none.json")}, 1},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(tt.args, nil, &stdout, &stderr); code != tt.want {
			t.Errorf("%s: code = %d, want %d (stderr: %s)", tt.name, code, tt.want, stderr.String())
		}
	}
}

func TestCheck(t *testing.T) {
	in := filepath.Join(t.TempDir(), "result.json")
	writeSample(t, in)

	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"met", []string{"-t", "p95<100ms", "-t", "error_rate<1%", in}, 0, "OK: all 2 met"},
		{"not met", []string{"-t", "p95<1ms", "-t", "error_rate<1%", in}, 1, "FAIL  p95<1ms"},
		{"select a step", []string{"-t", "p95{step=login}<100ms", in}, 0, "[checkout/login]"},
		{"no such step", []string{"-t", "p95{step=nothing}<100ms", in}, 1, "no matching step"},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		code := run(append([]string{"check"}, tt.args...), nil, &stdout, &stderr)
		if code != tt.code || !strings.Contains(stdout.String(), tt.want) {
			t.Errorf("%s: code = %d (want %d), stdout = %s, stderr = %s", tt.name, code, tt.code, stdout.String(), stderr.String())
		}
	}
}

func TestCheckUsageErrors(t *testing.T) {
	in := filepath.Join(t.TempDir(), "result.json")
	writeSample(t, in)

	for name, args := range map[string][]string{
		"no threshold":         {in},
		"no result file":       {"-t", "p95<1s"},
		"unreadable threshold": {"-t", "p95", in},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"check"}, args...), nil, &stdout, &stderr); code != 2 {
			t.Errorf("%s: code = %d, want 2 (stderr = %s)", name, code, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "-t", "p95<1s", filepath.Join(t.TempDir(), "none.json")}, nil, &stdout, &stderr); code != 1 {
		t.Errorf("code = %d when the file cannot be opened, want 1", code)
	}
}

// writeSampleRaw writes the same content as writeSample to path, as raw data (JSON Lines).
func writeSampleRaw(t *testing.T, path string) {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := &record.Raw{
		StartedAt: t0, Elapsed: time.Second,
		Requests:   []record.Record{{Scenario: "checkout", Step: "login", Code: 200, Start: t0, Latency: 12 * time.Millisecond}},
		Steps:      []record.StepRecord{{Scenario: "checkout", Step: "login", Start: t0, Duration: 15 * time.Millisecond}},
		Iterations: []record.IterationRecord{{Scenario: "checkout", Start: t0, Duration: 20 * time.Millisecond}},
	}
	var buf bytes.Buffer
	if err := record.WriteRaw(&buf, raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReportRaw(t *testing.T) {
	in := filepath.Join(t.TempDir(), "raw.jsonl")
	writeSampleRaw(t, in)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"report", "-raw", "-format", "text", in}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "requests:   1 ") || !strings.Contains(out, "checkout") {
		t.Errorf("not aggregated from raw data:\n%s", out)
	}

	// Passing raw data without -raw is an error, since it is not aggregated JSON.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"report", in}, nil, &stdout, &stderr); code != 1 {
		t.Errorf("code = %d without -raw, want 1", code)
	}
}

func TestReportRawStdinToJSONThenCheck(t *testing.T) {
	in := filepath.Join(t.TempDir(), "raw.jsonl")
	writeSampleRaw(t, in)
	data, _ := os.ReadFile(in)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"report", "-raw", "-format", "json", "-"}, bytes.NewReader(data), &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	// Raw data → aggregated JSON → threshold check can be chained.
	var out bytes.Buffer
	if code := run([]string{"check", "-t", "p95<100ms", "-"}, &stdout, &out, &stderr); code != 0 {
		t.Fatalf("check: code = %d, stdout = %s, stderr = %s", code, out.String(), stderr.String())
	}
}

func TestCheckAndMergeRaw(t *testing.T) {
	in := filepath.Join(t.TempDir(), "raw.jsonl")
	writeSampleRaw(t, in)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "-raw", "-t", "p95<1ms", in}, nil, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "FAIL  p95<1ms") {
		t.Errorf("check -raw: code = %d, stdout = %s", code, stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"merge", "-raw", "-format", "text", in, in}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "requests:   2 ") {
		t.Errorf("merge -raw: code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
}

func TestReadErrorsHaveSinglePrefix(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"x"`), 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"report", bad}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stderr.String(), "jotaro: jotaro:") {
		t.Errorf("prefix doubled: %s", stderr.String())
	}
}

// writeLatency writes a result whose only request took lat to path.
func writeLatency(t *testing.T, path string, lat time.Duration) {
	t.Helper()
	rep := report.BuildReport(
		[]record.Record{{Scenario: "checkout", Step: "login", Code: 200, Latency: lat}},
		nil, nil, time.Second,
	)
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompare(t *testing.T) {
	dir := t.TempDir()
	base, cur := filepath.Join(dir, "base.json"), filepath.Join(dir, "new.json")
	writeLatency(t, base, 100*time.Millisecond)
	writeLatency(t, cur, 150*time.Millisecond)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"compare", base, cur}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "+50.0%") {
		t.Fatalf("diff missing: %s", stdout.String())
	}

	stdout.Reset()
	if code := run([]string{"compare", "-max-p95-regression", "10", base, cur}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("gate code = %d, want 1", code)
	}
	if code := run([]string{"compare", "-max-p95-regression", "60", base, cur}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("loose gate code = %d, want 0", code)
	}
}

func TestCompareErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"compare", "only-one.json"}, nil, &stdout, &stderr); code != 2 {
		t.Errorf("usage code = %d, want 2", code)
	}
	if code := run([]string{"compare", "missing-a.json", "missing-b.json"}, nil, &stdout, &stderr); code != 1 {
		t.Errorf("missing file code = %d, want 1", code)
	}
}

func TestExport(t *testing.T) {
	in := filepath.Join(t.TempDir(), "raw.jsonl")
	writeSampleRaw(t, in)

	for _, tc := range []struct {
		format string
		want   string
	}{
		{"vegeta", `"attack":`},
		{"csv", "second,requests,"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"export", "-format", tc.format, in}, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("%s: exit code %d: %s", tc.format, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%s: output lacks %q:\n%s", tc.format, tc.want, stdout.String())
		}
	}
}

func TestExportUsage(t *testing.T) {
	in := filepath.Join(t.TempDir(), "raw.jsonl")
	writeSampleRaw(t, in)
	for _, args := range [][]string{
		{"export", in},                   // no format
		{"export", "-format", "xml", in}, // unknown format
		{"export", "-format", "csv"},     // no input
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, nil, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export", "-format", "csv", filepath.Join(t.TempDir(), "missing")}, nil, &stdout, &stderr); code != 1 {
		t.Errorf("missing file: exit code %d, want 1", code)
	}
}

// TestRawCommandsRejectNonRawInput checks that an aggregated JSON result or an empty input given where raw data is
// wanted is an error with a hint, and not a silent empty result.
func TestRawCommandsRejectNonRawInput(t *testing.T) {
	dir := t.TempDir()
	aggregated := filepath.Join(dir, "result.json")
	writeSample(t, aggregated)
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"export csv, aggregated":    {"export", "-format", "csv", aggregated},
		"export vegeta, aggregated": {"export", "-format", "vegeta", aggregated},
		"export csv, empty":         {"export", "-format", "csv", empty},
		"export vegeta, empty":      {"export", "-format", "vegeta", empty},
		"check -raw, aggregated":    {"check", "-raw", "-t", "p95<1s", aggregated},
		"check -raw, empty":         {"check", "-raw", "-t", "p95<1s", empty},
		"report -raw, aggregated":   {"report", "-raw", aggregated},
		"merge -raw, aggregated":    {"merge", "-raw", aggregated},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, nil, &stdout, &stderr); code != 1 {
			t.Errorf("%s: code = %d, want 1", name, code)
		}
		if !strings.Contains(stderr.String(), "Plan.Raw") {
			t.Errorf("%s: stderr should point to the output of Plan.Raw: %q", name, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: stdout should be empty, got %q", name, stdout.String())
		}
	}

	// The same input on stdin.
	data, _ := os.ReadFile(aggregated)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export", "-format", "csv", "-"}, bytes.NewReader(data), &stdout, &stderr); code != 1 {
		t.Errorf("export from stdin: code = %d, want 1", code)
	}
}

// TestRawWithoutRequests checks that a valid raw stream of a run that made no requests (a meta line only) still works.
func TestRawWithoutRequests(t *testing.T) {
	in := filepath.Join(t.TempDir(), "raw.jsonl")
	if err := os.WriteFile(in, []byte(`{"type":"meta","version":1,"open":false,"dropped":0,"elapsed_ns":1000000000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export", "-format", "csv", in}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("export: code = %d, stderr = %s", code, stderr.String())
	}
	if code := run([]string{"report", "-raw", "-format", "text", in}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("report -raw: code = %d, stderr = %s", code, stderr.String())
	}
}

// TestMergeCompareStdin pins what the usage says: merge and compare accept "-" (standard input) for one input.
func TestMergeCompareStdin(t *testing.T) {
	in := filepath.Join(t.TempDir(), "result.json")
	writeSample(t, in)
	data, _ := os.ReadFile(in)

	for _, args := range [][]string{
		{"merge", "-format", "text", "-", in},
		{"merge", "-format", "text", in, "-"},
		{"compare", "-", in},
		{"compare", in, "-"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, bytes.NewReader(data), &stdout, &stderr); code != 0 {
			t.Errorf("%v: code = %d, stderr = %s", args, code, stderr.String())
		}
	}
	for _, want := range []string{"merge", "compare"} {
		if !strings.Contains(usage, "only one input can be \"-\"") || !strings.Contains(usage, want) {
			t.Errorf("usage should say that %s reads standard input", want)
		}
	}
}

// Raw data of a killed run (last line cut off) is still reported, with a warning on stderr.
func TestReportRawTruncated(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "raw.jsonl")
	writeSampleRaw(t, full)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(dir, "cut.jsonl")
	if err := os.WriteFile(cut, append(bytes.TrimRight(data, "\n"), []byte("\n{\"type\":\"request\",\"scen")...), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"report", "-raw", "-format", "text", cut}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "requests:   1 ") {
		t.Errorf("the records before the cut line are missing:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning") || !strings.Contains(stderr.String(), "middle of a line") {
		t.Errorf("no truncation warning: %q", stderr.String())
	}

	// A complete file prints no warning.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"report", "-raw", "-format", "text", full}, nil, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Errorf("code = %d, stderr = %q", code, stderr.String())
	}
}

// runCLI runs the CLI and returns the exit code with what it wrote to standard output and standard error.
func runCLI(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestFlagsAfterFileNames pins that flags may follow the file names: the flag package alone would take
// "-format" and "text" for two more files.
func TestFlagsAfterFileNames(t *testing.T) {
	dir := t.TempDir()
	in, other := filepath.Join(dir, "result.json"), filepath.Join(dir, "other.json")
	writeSample(t, in)
	writeSample(t, other)
	raw := filepath.Join(dir, "raw.jsonl")
	writeSampleRaw(t, raw)
	data, _ := os.ReadFile(in)

	t.Run("report -format after the file", func(t *testing.T) {
		code, stdout, stderr := runCLI(t, "", "report", in, "-format", "text")
		if code != 0 || !strings.Contains(stdout, "=== Summary ===") {
			t.Fatalf("code = %d, stdout = %.100s, stderr = %s", code, stdout, stderr)
		}
	})
	t.Run("report -format=text after the file", func(t *testing.T) {
		code, stdout, _ := runCLI(t, "", "report", in, "--format=text")
		if code != 0 || !strings.Contains(stdout, "=== Summary ===") {
			t.Fatalf("code = %d, stdout = %.100s", code, stdout)
		}
	})
	t.Run("report stdin then flag", func(t *testing.T) {
		code, stdout, stderr := runCLI(t, string(data), "report", "-", "-format", "text")
		if code != 0 || !strings.Contains(stdout, "=== Summary ===") {
			t.Fatalf("code = %d, stdout = %.100s, stderr = %s", code, stdout, stderr)
		}
	})
	t.Run("report -o and bool flag after the file", func(t *testing.T) {
		out := filepath.Join(dir, "out.txt")
		if code, _, stderr := runCLI(t, "", "report", raw, "-raw", "-format", "text", "-o", out); code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, stderr)
		}
		if b, err := os.ReadFile(out); err != nil || !strings.Contains(string(b), "=== Summary ===") {
			t.Fatalf("output = %.100s, err = %v", b, err)
		}
	})
	t.Run("check -t after the file", func(t *testing.T) {
		code, stdout, stderr := runCLI(t, "", "check", in, "-t", "p95<1s")
		if code != 0 || !strings.Contains(stdout, "p95") {
			t.Fatalf("code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
		}
		if code, _, _ := runCLI(t, "", "check", in, "-t", "p95<1ns"); code != 1 {
			t.Fatalf("failing threshold: code = %d, want 1", code)
		}
	})
	t.Run("merge flags between files", func(t *testing.T) {
		code, stdout, stderr := runCLI(t, "", "merge", in, "-format", "text", other)
		if code != 0 || !strings.Contains(stdout, "=== Summary ===") {
			t.Fatalf("code = %d, stdout = %.100s, stderr = %s", code, stdout, stderr)
		}
	})
	t.Run("compare gate after the files", func(t *testing.T) {
		if code, _, stderr := runCLI(t, "", "compare", in, other, "-max-p95-regression", "10"); code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, stderr)
		}
	})
	t.Run("export", func(t *testing.T) {
		code, stdout, stderr := runCLI(t, "", "export", raw, "-format", "csv")
		if code != 0 || !strings.Contains(stdout, "second,requests,") {
			t.Fatalf("code = %d, stdout = %.100s, stderr = %s", code, stdout, stderr)
		}
	})
	t.Run("-- ends the flags", func(t *testing.T) {
		// "-format" after "--" is a file name, so the command sees two files.
		code, _, stderr := runCLI(t, "", "report", "-format", "text", "--", in, "-format")
		if code != 2 || !strings.Contains(stderr, "expected one result file, got 2") {
			t.Fatalf("code = %d, stderr = %s", code, stderr)
		}
	})
	t.Run("a flag without its value is reported", func(t *testing.T) {
		// The "--" that separates the file names must not become the value of the last flag.
		for _, args := range [][]string{
			{"report", in, "-o"},
			{"report", "-o"},
			{"check", in, "-t"},
			{"merge", in, other, "-format"},
			{"export", raw, "-format"},
		} {
			cwd := t.TempDir()
			t.Chdir(cwd)
			code, stdout, stderr := runCLI(t, "", args...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, "flag needs an argument") {
				t.Errorf("%v: code = %d, stdout = %.60q, stderr = %s", args, code, stdout, stderr)
			}
			if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
				t.Errorf("%v: created files in the working directory: %v", args, entries)
			}
		}
	})
	t.Run("an unknown flag after the file is reported", func(t *testing.T) {
		code, _, stderr := runCLI(t, "", "report", in, "-nope")
		if code != 2 || !strings.Contains(stderr, "flag provided but not defined: -nope") {
			t.Fatalf("code = %d, stderr = %s", code, stderr)
		}
	})
}

// TestUsageErrorsSayWhy pins that a usage error starts with a sentence about the cause, then shows the usage,
// all on standard error.
func TestUsageErrorsSayWhy(t *testing.T) {
	in := filepath.Join(t.TempDir(), "result.json")
	writeSample(t, in)
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	writeSampleRaw(t, raw)

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"report without a file", []string{"report"}, "jotaro report: no input file given"},
		{"report with three files", []string{"report", "a", "b", "c"}, "jotaro report: expected one result file, got 3: a b c"},
		{"merge without a file", []string{"merge", "-format", "text"}, "jotaro merge: no result file given"},
		{"check without a threshold", []string{"check", in}, "jotaro check: no threshold given (-t)"},
		{"check without a file", []string{"check", "-t", "p95<1s"}, "jotaro check: no input file given"},
		{"compare with one file", []string{"compare", in}, "jotaro compare: expected two result files"},
		{"export without a format", []string{"export", raw}, "jotaro export: no output format given"},
		{"export without a file", []string{"export", "-format", "csv"}, "jotaro export: no input file given"},
		{"mcp with an argument", []string{"mcp", "extra"}, `jotaro mcp: mcp takes no arguments, got "extra"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, "", tc.args...)
			if code != 2 {
				t.Fatalf("code = %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.HasPrefix(stderr, tc.want) {
				t.Errorf("stderr should start with %q, got:\n%s", tc.want, stderr)
			}
			if !strings.Contains(stderr, "usage: jotaro "+tc.args[0]) {
				t.Errorf("stderr lacks the usage:\n%s", stderr)
			}
		})
	}
}

// TestHelpGoesToStdout pins that requested help is written to standard output and exits 0, wherever -h stands.
func TestHelpGoesToStdout(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"report", "-h"}, "usage: jotaro report"},
		{[]string{"report", "--help"}, "usage: jotaro report"},
		{[]string{"report", "result.json", "-h"}, "usage: jotaro report"},
		{[]string{"merge", "-h"}, "usage: jotaro merge"},
		{[]string{"check", "-h"}, "usage: jotaro check"},
		{[]string{"compare", "-help"}, "usage: jotaro compare"},
		{[]string{"export", "-h"}, "usage: jotaro export"},
		{[]string{"mcp", "-h"}, "usage: jotaro mcp"},
		{[]string{"version", "-h"}, "usage: jotaro version"},
		{[]string{"-h"}, "usage: jotaro <command>"},
		{[]string{"help"}, "usage: jotaro <command>"},
	} {
		code, stdout, stderr := runCLI(t, "", tc.args...)
		if code != 0 || stderr != "" || !strings.Contains(stdout, tc.want) {
			t.Errorf("%v: code = %d, stderr = %q, stdout = %q; want %q on stdout", tc.args, code, stderr, stdout, tc.want)
		}
	}
	// The flag list is part of the help.
	if _, stdout, _ := runCLI(t, "", "report", "-h"); !strings.Contains(stdout, "-format string") {
		t.Errorf("report -h lacks the flag list:\n%s", stdout)
	}
}

// TestHelpCommand pins that "jotaro help <command>" prints what "jotaro <command> -h" prints.
func TestHelpCommand(t *testing.T) {
	for _, name := range []string{"report", "merge", "check", "compare", "export", "mcp", "version"} {
		wantCode, wantOut, _ := runCLI(t, "", name, "-h")
		code, stdout, stderr := runCLI(t, "", "help", name)
		if code != wantCode || stdout != wantOut || stderr != "" {
			t.Errorf("help %s: code = %d, stderr = %q, stdout = %q; want %q", name, code, stderr, stdout, wantOut)
		}
		if !strings.Contains(stdout, "usage: jotaro "+name) {
			t.Errorf("help %s: no usage of the command:\n%s", name, stdout)
		}
	}
	code, stdout, stderr := runCLI(t, "", "help", "chek")
	if code != 2 || stdout != "" || !strings.Contains(stderr, `unknown command "chek"`) {
		t.Errorf("help chek: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

// TestUnknownCommandSuggestion pins the "did you mean" line, and that a far-off word gets none.
func TestUnknownCommandSuggestion(t *testing.T) {
	for in, want := range map[string]string{
		"chek":    `did you mean "check"?`,
		"reprot":  `did you mean "report"?`,
		"mrege":   `did you mean "merge"?`,
		"compar":  `did you mean "compare"?`,
		"exprot":  `did you mean "export"?`,
		"versoin": `did you mean "version"?`, //nolint:misspell // deliberate typo: the test feeds a misspelled command
	} {
		code, stdout, stderr := runCLI(t, "", in)
		if code != 2 || stdout != "" || !strings.Contains(stderr, `unknown command "`+in+`"; `+want) {
			t.Errorf("%s: code = %d, stdout = %q, stderr = %q; want %q", in, code, stdout, stderr, want)
		}
	}
	_, _, stderr := runCLI(t, "", "frobnicate")
	if strings.Contains(stderr, "did you mean") {
		t.Errorf("a far-off command should get no suggestion: %s", stderr)
	}
}

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"", "", 0}, {"a", "", 1}, {"check", "check", 0}, {"chek", "check", 1}, {"kitten", "sitting", 3}} {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestUsageMentionsFlexibleFlagOrder keeps the top-level usage in step with the behaviour.
func TestUsageMentionsFlexibleFlagOrder(t *testing.T) {
	if !strings.Contains(usage, "before or after the file names") || !strings.Contains(usage, "jotaro help <command>") {
		t.Errorf("usage should say that flags can follow the file names and mention help <command>:\n%s", usage)
	}
}
