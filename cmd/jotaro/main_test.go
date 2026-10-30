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
