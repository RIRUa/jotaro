package record

// Compatibility tests: raw data written by older versions must keep reading, and the lines that
// WriteRaw emits now are pinned by a golden file. The fixtures and how each one was produced are
// described in testdata/README.md. Nothing here depends on timing: every expected number is the
// one stored in the fixture.

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// update rewrites the golden file: go test ./record -run TestWriteRawGolden -update
var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// testdataBytes reads a file of testdata with line ends normalised to "\n", so that a checkout with
// CRLF line ends (Windows) does not matter.
func testdataBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// rawFixtures are the stored raw files; version is the RawVersion in their meta line.
var rawFixtures = []struct {
	file    string
	version int
	elapsed time.Duration // the last meta line wins
}{
	{"raw-v0.1.0.jsonl", 1, 390576167},
	{"raw-v0.1.2.jsonl", 1, 325594875},
	{"raw-v0.1.3.jsonl", 1, 329910458},
}

// TestRawFixtureVersionsCoverTheCurrentOne fails when RawVersion is raised without a fixture of the
// new version, so every version that can be read has a stored file.
func TestRawFixtureVersionsCoverTheCurrentOne(t *testing.T) {
	seen := map[int]bool{}
	for _, f := range rawFixtures {
		seen[f.version] = true
	}
	for v := 1; v <= RawVersion; v++ {
		if !seen[v] {
			t.Errorf("no fixture of raw version %d in testdata (see testdata/README.md)", v)
		}
	}
}

// TestReadRawOldFormats reads stored raw data of every release that has it. All of them are
// RawVersion 1; they differ in how the file was written (v0.1.3 streams: a first meta line when the
// run starts and a second one with the final elapsed time at the end).
func TestReadRawOldFormats(t *testing.T) {
	for _, tc := range rawFixtures {
		t.Run(tc.file, func(t *testing.T) {
			data := testdataBytes(t, tc.file)
			if want := fmt.Sprintf(`{"type":"meta","version":%d,`, tc.version); !bytes.HasPrefix(data, []byte(want)) {
				t.Errorf("the first line of %s does not start with %s", tc.file, want)
			}
			raw, err := ReadRaw(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if raw.Open || raw.Dropped != 0 || raw.Truncated {
				t.Errorf("Open=%v Dropped=%d Truncated=%v, want a closed, complete run without drops", raw.Open, raw.Dropped, raw.Truncated)
			}
			if raw.StartedAt.IsZero() {
				t.Error("StartedAt is zero")
			}
			if raw.Elapsed != tc.elapsed {
				t.Errorf("Elapsed = %v, want %v", raw.Elapsed, tc.elapsed)
			}
			if len(raw.Requests) != 24 || len(raw.Steps) != 24 || len(raw.Iterations) != 8 || len(raw.Health) != 1 {
				t.Fatalf("requests/steps/iterations/health = %d/%d/%d/%d, want 24/24/8/1",
					len(raw.Requests), len(raw.Steps), len(raw.Iterations), len(raw.Health))
			}

			type stepCode struct {
				step string
				code int
			}
			byCode := map[stepCode]int{}
			conns := map[ConnKind]int{}
			var in, out int64
			for _, r := range raw.Requests {
				if r.Scenario != "browse" || r.Method == "" || r.URL == "" || r.Err != "" {
					t.Errorf("request = %+v, want scenario browse, a method, a URL and no error", r)
				}
				if r.Start.IsZero() || r.Scheduled.IsZero() || r.Latency <= 0 {
					t.Errorf("request has no start, scheduled time or latency: %+v", r)
				}
				byCode[stepCode{r.Step, r.Code}]++
				conns[r.Conn]++
				in += r.BytesIn
				out += r.BytesOut
			}
			wantCodes := map[stepCode]int{{"list", 200}: 8, {"order", 201}: 8, {"health", 500}: 8}
			if !reflect.DeepEqual(byCode, wantCodes) {
				t.Errorf("requests per step and status = %v, want %v", byCode, wantCodes)
			}
			if want := map[ConnKind]int{ConnNew: 2, ConnReused: 22}; !reflect.DeepEqual(conns, want) {
				t.Errorf("connections = %v, want %v", conns, want)
			}
			if in != 192 || out != 80 {
				t.Errorf("bytes in/out = %d/%d, want 192/80", in, out)
			}

			type stepErr struct {
				step string
				err  ErrKind
			}
			steps := map[stepErr]int{}
			for _, s := range raw.Steps {
				if s.Scenario != "browse" || s.Duration <= 0 {
					t.Errorf("step = %+v, want scenario browse and a duration", s)
				}
				steps[stepErr{s.Step, s.Err}]++
			}
			wantSteps := map[stepErr]int{{"list", ""}: 8, {"order", ""}: 8, {"health", ErrExpect}: 8}
			if !reflect.DeepEqual(steps, wantSteps) {
				t.Errorf("steps = %v, want %v", steps, wantSteps)
			}
			for _, it := range raw.Iterations {
				if it.Scenario != "browse" || it.Err != ErrExpect || it.Duration <= 0 || it.Think <= 0 {
					t.Errorf("iteration = %+v, want scenario browse, expect_mismatch, a duration and think time", it)
				}
			}
			if h := raw.Health[0]; h.Second != 0 || h.Goroutines != 9 || h.CPU < 0 || h.CPU > 1 {
				t.Errorf("health = %+v, want second 0, 9 goroutines and CPU between 0 and 1", h)
			}
		})
	}
}

// TestOldRawWriteAndReadAgain writes what an old file was read as in the current format and reads
// that back: the data survives the trip unchanged.
func TestOldRawWriteAndReadAgain(t *testing.T) {
	for _, f := range rawFixtures {
		file := f.file
		t.Run(file, func(t *testing.T) {
			raw, err := ReadRaw(bytes.NewReader(testdataBytes(t, file)))
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := WriteRaw(&buf, raw); err != nil {
				t.Fatal(err)
			}
			again, err := ReadRaw(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, raw) {
				t.Errorf("changed after WriteRaw and ReadRaw")
			}
		})
	}
}

// goldenRaw is a fixed run that uses every field of the raw data.
func goldenRaw() *Raw {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ms := time.Millisecond
	return &Raw{
		Open: true, Dropped: 3, StartedAt: t0, Elapsed: 2500 * ms,
		Requests: []Record{
			{Scenario: "shop", Step: "login", UserID: 1, Iteration: 0, Method: "POST", URL: "http://x/login", Code: 200,
				Scheduled: t0, Start: t0.Add(2 * ms), Latency: 40 * ms, Conn: ConnNew,
				DNS: 3 * ms, Connect: 5 * ms, TLS: 9 * ms, TTFB: 30 * ms, BytesOut: 20, BytesIn: 100},
			{Scenario: "shop", Step: "order", UserID: 2, Iteration: 1, Method: "POST", URL: "http://x/orders", Err: ErrTimeout,
				Start: t0.Add(1300 * ms), Latency: 1500 * ms, Conn: ConnReused, BytesOut: 30},
		},
		Steps: []StepRecord{
			{Scenario: "shop", Step: "login", UserID: 1, Iteration: 0, Start: t0, Duration: 45 * ms},
			{Scenario: "shop", Step: "order", UserID: 2, Iteration: 1, Start: t0.Add(1300 * ms), Duration: 1500 * ms, Err: ErrTimeout},
		},
		Iterations: []IterationRecord{
			{Scenario: "shop", UserID: 2, Iteration: 1, Start: t0.Add(1200 * ms), Duration: 1700 * ms, Think: 100 * ms, Err: ErrExpect},
		},
		Health: []HealthPoint{{Second: 0, Goroutines: 12, CPU: 0.25}, {Second: 1, Goroutines: 31, CPU: -1}},
	}
}

// TestWriteRawGolden compares what WriteRaw emits for a fixed run with a committed file. A failure
// means the lines changed. If that was intended: update docs/result.md (and result_jp.md), raise
// RawVersion when the change is not backward compatible for readers, add a fixture of the new
// version (see testdata/README.md), and run
//
//	go test ./record -run TestWriteRawGolden -update
//
// so that the new file shows up in the review as a diff.
func TestWriteRawGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRaw(&buf, goldenRaw()); err != nil {
		t.Fatal(err)
	}
	const name = "golden-current.jsonl"
	if *update {
		if err := os.WriteFile(filepath.Join("testdata", name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := testdataBytes(t, name); !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("WriteRaw output changed (RawVersion = %d). Update docs/result.md and the fixtures, then run\n"+
			"go test ./record -run TestWriteRawGolden -update\n--- got\n%s\n--- want\n%s", RawVersion, buf.Bytes(), want)
	}

	got, err := ReadRaw(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, goldenRaw()) {
		t.Errorf("the golden raw data does not read back as the run it was written from")
	}
}
