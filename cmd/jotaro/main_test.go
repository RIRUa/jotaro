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

// writeSample は小さな結果を JSON にして path に書く。
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
		t.Fatalf("HTML になっていない: %.200s", b)
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
		t.Fatalf("text になっていない: %s", stdout.String())
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
		{"引数なし", nil, 2},
		{"未知のコマンド", []string{"nope"}, 2},
		{"入力なし", []string{"report"}, 2},
		{"未知の形式", []string{"report", "-format", "csv", bad}, 2},
		{"ファイルが無い", []string{"report", filepath.Join(dir, "none.json")}, 1},
		{"未対応の version", []string{"report", bad}, 1},
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
		t.Fatalf("結合結果が JSON として読めない: %v", err)
	}
	if rep.Requests.Count != 2 || rep.Scenarios[0].Iterations.Count != 2 {
		t.Fatalf("2 件分が合計されていない: %+v", rep.Requests)
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
		{"入力なし", []string{"merge"}, 2},
		{"未知の形式", []string{"merge", "-format", "csv", ok}, 2},
		{"ファイルが無い", []string{"merge", ok, filepath.Join(dir, "none.json")}, 1},
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
		{"満たす", []string{"-t", "p95<100ms", "-t", "error_rate<1%", in}, 0, "OK: all 2 met"},
		{"満たさない", []string{"-t", "p95<1ms", "-t", "error_rate<1%", in}, 1, "FAIL  p95<1ms"},
		{"ステップを絞る", []string{"-t", "p95{step=login}<100ms", in}, 0, "[checkout/login]"},
		{"ステップがない", []string{"-t", "p95{step=nothing}<100ms", in}, 1, "no matching step"},
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
		"しきい値がない":   {in},
		"結果ファイルがない": {"-t", "p95<1s"},
		"しきい値が読めない": {"-t", "p95", in},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"check"}, args...), nil, &stdout, &stderr); code != 2 {
			t.Errorf("%s: code = %d, want 2 (stderr = %s)", name, code, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "-t", "p95<1s", filepath.Join(t.TempDir(), "none.json")}, nil, &stdout, &stderr); code != 1 {
		t.Errorf("ファイルが開けないとき code = %d, want 1", code)
	}
}

// writeSampleRaw は writeSample と同じ内容を、生データ（JSON Lines）として path に書く。
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
		t.Errorf("生データから集計できていない:\n%s", out)
	}

	// -raw を付けずに生データを渡すと、集計済み JSON ではないのでエラーになる。
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"report", in}, nil, &stdout, &stderr); code != 1 {
		t.Errorf("-raw 無しで code = %d, want 1", code)
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
	// 生データ → 集計済み JSON → しきい値判定、と続けられる。
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
		t.Errorf("接頭辞が二重: %s", stderr.String())
	}
}
