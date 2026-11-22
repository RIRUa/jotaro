package report

// Compatibility tests: stored result files of older versions must keep reading, and the shape that
// WriteJSON emits now is pinned by a golden file. The fixtures and how each one was produced are
// described in testdata/README.md. Nothing here depends on timing: every expected number is the
// one stored in the fixture.

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// update rewrites the golden files: go test ./report -run TestWriteJSONGolden -update
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

func readFixture(t *testing.T, name string) *Report {
	t.Helper()
	rep, err := ReadJSON(bytes.NewReader(testdataBytes(t, name)))
	if err != nil {
		t.Fatalf("ReadJSON(%s): %v", name, err)
	}
	return rep
}

// stepWant is a step of the run that every fixture contains: one run of the scenario "browse" with 8 iterations of
// the steps list (200), order (201) and health (always 500, so its check fails). The version, the
// latencies and the time series differ per file.
type stepWant struct {
	name   string
	count  int
	failed int
	errors map[record.ErrKind]int
	status map[string]int
}

var fixtureSteps = []stepWant{
	{"list", 8, 0, nil, map[string]int{"2xx": 8}},
	{"order", 8, 0, nil, map[string]int{"2xx": 8}},
	{"health", 8, 8, map[record.ErrKind]int{record.ErrExpect: 8}, map[string]int{"5xx": 8}},
}

type jsonFixture struct {
	file      string
	version   int           // "version" in the file
	startedAt bool          // the file has started_at (version 2 and later)
	buckets   bool          // the latencies carry histogram buckets (version 2 and later)
	elapsed   time.Duration // elapsed_ns
	p50       time.Duration // requests.latency
	p95       time.Duration
	max       time.Duration
	iterP95   time.Duration // iterations.total.p95
	series    int           // number of points in series (version 3)
	health    int           // number of points in health (version 3)
	connNew   int           // requests.conn_new (version 3)
	conReused int           // requests.conn_reused (version 3)
}

var jsonFixtures = []jsonFixture{
	{file: "v1-derived.json", version: 1,
		elapsed: 324382875, p50: 125541, p95: 860167, max: 906541, iterP95: 82503125},
	{file: "v2-3f79071.json", version: 2, startedAt: true, buckets: true,
		elapsed: 324382875, p50: 125541, p95: 860167, max: 906541, iterP95: 82503125},
	{file: "v3-v0.1.0.json", version: 3, startedAt: true, buckets: true,
		elapsed: 390576167, p50: 437292, p95: 2844375, max: 43696208, iterP95: 126760375,
		series: 1, health: 1, connNew: 2, conReused: 22},
	{file: "v3-v0.1.2.json", version: 3, startedAt: true, buckets: true,
		elapsed: 325594875, p50: 122000, p95: 850000, max: 917625, iterP95: 81765667,
		series: 1, health: 1, connNew: 2, conReused: 22},
	{file: "v3-v0.1.3.json", version: 3, startedAt: true, buckets: true,
		elapsed: 329910458, p50: 299650, p95: 1167547, max: 1188125, iterP95: 82685739,
		series: 1, health: 1, connNew: 2, conReused: 22},
}

// TestReadJSONOldFormats reads a stored result of every format version and checks the numbers
// that were written into it.
func TestReadJSONOldFormats(t *testing.T) {
	for _, f := range jsonFixtures {
		t.Run(f.file, func(t *testing.T) {
			rep := readFixture(t, f.file)

			if rep.Open || rep.Dropped != 0 {
				t.Errorf("Open=%v Dropped=%d, want a closed run without drops", rep.Open, rep.Dropped)
			}
			if got := !rep.StartedAt.IsZero(); got != f.startedAt {
				t.Errorf("StartedAt set = %v, want %v (%v)", got, f.startedAt, rep.StartedAt)
			}
			if rep.Elapsed != f.elapsed {
				t.Errorf("Elapsed = %v, want %v", rep.Elapsed, f.elapsed)
			}

			q := rep.Requests
			if q.Count != 24 || q.Latency.Count != 24 {
				t.Errorf("requests = %d (latency %d), want 24", q.Count, q.Latency.Count)
			}
			if want := map[string]int{"2xx": 16, "5xx": 8}; !reflect.DeepEqual(q.Status, want) {
				t.Errorf("Status = %v, want %v", q.Status, want)
			}
			if len(q.Errors) != 0 {
				t.Errorf("Errors = %v, want none", q.Errors)
			}
			if q.Latency.P50 != f.p50 || q.Latency.P95 != f.p95 || q.Latency.Max != f.max {
				t.Errorf("latency p50/p95/max = %v/%v/%v, want %v/%v/%v",
					q.Latency.P50, q.Latency.P95, q.Latency.Max, f.p50, f.p95, f.max)
			}
			if q.BytesIn != 192 || q.BytesOut != 80 {
				t.Errorf("bytes in/out = %d/%d, want 192/80", q.BytesIn, q.BytesOut)
			}
			if q.NewConns != f.connNew || q.ReusedConns != f.conReused {
				t.Errorf("conn new/reused = %d/%d, want %d/%d", q.NewConns, q.ReusedConns, f.connNew, f.conReused)
			}
			if got := len(q.Latency.hist) > 0; got != f.buckets {
				t.Errorf("latency has buckets = %v, want %v", got, f.buckets)
			}

			if len(rep.Scenarios) != 1 {
				t.Fatalf("scenarios = %d, want 1", len(rep.Scenarios))
			}
			sc := rep.Scenarios[0]
			if sc.Name != "browse" || sc.Iterations.Count != 8 || sc.Iterations.Failed != 8 {
				t.Errorf("scenario %q iterations %d failed %d, want browse 8 8", sc.Name, sc.Iterations.Count, sc.Iterations.Failed)
			}
			if want := map[record.ErrKind]int{record.ErrExpect: 8}; !reflect.DeepEqual(sc.Iterations.Errors, want) {
				t.Errorf("iteration errors = %v, want %v", sc.Iterations.Errors, want)
			}
			if sc.Iterations.Total.P95 != f.iterP95 {
				t.Errorf("iteration total p95 = %v, want %v", sc.Iterations.Total.P95, f.iterP95)
			}
			if len(sc.Steps) != len(fixtureSteps) {
				t.Fatalf("steps = %d, want %d", len(sc.Steps), len(fixtureSteps))
			}
			for i, w := range fixtureSteps {
				s := sc.Steps[i]
				if s.Name != w.name || s.Count != w.count || s.Failed != w.failed || !reflect.DeepEqual(s.Errors, w.errors) {
					t.Errorf("step %d = %q count %d failed %d errors %v, want %q %d %d %v",
						i, s.Name, s.Count, s.Failed, s.Errors, w.name, w.count, w.failed, w.errors)
				}
				if s.Requests.Count != 8 || !reflect.DeepEqual(s.Requests.Status, w.status) {
					t.Errorf("step %q requests = %d %v, want 8 %v", s.Name, s.Requests.Count, s.Requests.Status, w.status)
				}
			}

			if len(rep.Series) != f.series || len(rep.Health) != f.health {
				t.Errorf("series/health points = %d/%d, want %d/%d", len(rep.Series), len(rep.Health), f.series, f.health)
			}
			for _, p := range rep.Series {
				if p.Requests != 24 || p.Errors != 0 || p.HTTP5xx != 8 || p.NewConns != 2 || p.ReusedConns != 22 || p.Concurrency != 2 {
					t.Errorf("series point = %+v, want 24 requests, 8 5xx, 2 new, 22 reused, concurrency 2", p)
				}
			}
			for _, p := range rep.Health {
				if p.Second != 0 || p.Goroutines != 9 {
					t.Errorf("health point = %+v, want second 0 and 9 goroutines", p)
				}
			}
		})
	}
}

// TestOldFormatsWriteAndReadAgain writes what an old file was read as in the current format and
// reads that back: an old result stays usable (for jotaro report / compare / merge, which read
// and write the current format) and the numbers do not change on the way.
func TestOldFormatsWriteAndReadAgain(t *testing.T) {
	for _, f := range jsonFixtures {
		t.Run(f.file, func(t *testing.T) {
			rep := readFixture(t, f.file)
			var buf bytes.Buffer
			if err := rep.WriteJSON(&buf); err != nil {
				t.Fatal(err)
			}
			again, err := ReadJSON(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, rep) {
				t.Errorf("changed after WriteJSON and ReadJSON:\n got %+v\nwant %+v", again, rep)
			}
		})
	}
}

// TestMergeOldAndNew merges every old result with the newest one. The counts add up whatever the
// format version of each side is.
func TestMergeOldAndNew(t *testing.T) {
	newest := readFixture(t, "v3-v0.1.3.json")
	for _, f := range jsonFixtures {
		t.Run(f.file, func(t *testing.T) {
			old := readFixture(t, f.file)
			merged, err := MergeReports(old, newest)
			if err != nil {
				t.Fatal(err)
			}
			if merged.Requests.Count != 48 {
				t.Errorf("requests = %d, want 48", merged.Requests.Count)
			}
			if want := map[string]int{"2xx": 32, "5xx": 16}; !reflect.DeepEqual(merged.Requests.Status, want) {
				t.Errorf("Status = %v, want %v", merged.Requests.Status, want)
			}
			if len(merged.Scenarios) != 1 {
				t.Fatalf("scenarios = %d, want 1", len(merged.Scenarios))
			}
			sc := merged.Scenarios[0]
			if sc.Iterations.Count != 16 || sc.Iterations.Failed != 16 {
				t.Errorf("iterations %d failed %d, want 16 16", sc.Iterations.Count, sc.Iterations.Failed)
			}
			for i, w := range fixtureSteps {
				s := sc.Steps[i]
				if s.Name != w.name || s.Count != 2*w.count || s.Failed != 2*w.failed {
					t.Errorf("step %d = %q count %d failed %d, want %q %d %d", i, s.Name, s.Count, s.Failed, w.name, 2*w.count, 2*w.failed)
				}
			}
			// The merged result is valid JSON of the current version.
			var buf bytes.Buffer
			if err := merged.WriteJSON(&buf); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadJSON(&buf); err != nil {
				t.Errorf("the merged result does not read back: %v", err)
			}
		})
	}
}

// goldenReport is a fixed run that uses every field the JSON has: two scenarios, steps with and
// without errors, a failed request, a reused and a new connection with every phase, the time series
// and the health of the load generator.
func goldenReport() *Report {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ms := time.Millisecond
	raw := &record.Raw{
		Open: true, Dropped: 3, StartedAt: t0, Elapsed: 2500 * ms,
		Requests: []record.Record{
			{Scenario: "shop", Step: "login", UserID: 1, Method: "POST", URL: "http://x/login", Code: 200,
				Scheduled: t0, Start: t0.Add(2 * ms), Latency: 40 * ms, Conn: record.ConnNew,
				DNS: 3 * ms, Connect: 5 * ms, TLS: 9 * ms, TTFB: 30 * ms, BytesOut: 20, BytesIn: 100},
			{Scenario: "shop", Step: "list", UserID: 1, Method: "GET", URL: "http://x/items", Code: 200,
				Scheduled: t0.Add(100 * ms), Start: t0.Add(100 * ms), Latency: 12 * ms, Conn: record.ConnReused, TTFB: 10 * ms, BytesIn: 2048},
			{Scenario: "shop", Step: "list", UserID: 2, Method: "GET", URL: "http://x/items", Code: 503,
				Start: t0.Add(1200 * ms), Latency: 900 * ms, Conn: record.ConnReused, TTFB: 880 * ms, BytesIn: 10},
			{Scenario: "shop", Step: "order", UserID: 2, Method: "POST", URL: "http://x/orders", Err: record.ErrTimeout,
				Start: t0.Add(1300 * ms), Latency: 1500 * ms, Conn: record.ConnReused, BytesOut: 30},
			{Scenario: "admin", Step: "stats", UserID: 3, Method: "GET", URL: "http://x/stats", Code: 404,
				Start: t0.Add(1500 * ms), Latency: 7 * ms, Conn: record.ConnNew, Connect: 2 * ms},
		},
		Steps: []record.StepRecord{
			{Scenario: "shop", Step: "login", UserID: 1, Start: t0, Duration: 45 * ms},
			{Scenario: "shop", Step: "list", UserID: 1, Start: t0.Add(100 * ms), Duration: 14 * ms},
			{Scenario: "shop", Step: "list", UserID: 2, Start: t0.Add(1200 * ms), Duration: 905 * ms, Err: record.ErrExpect},
			{Scenario: "shop", Step: "order", UserID: 2, Start: t0.Add(1300 * ms), Duration: 1500 * ms, Err: record.ErrTimeout},
			{Scenario: "admin", Step: "stats", UserID: 3, Start: t0.Add(1500 * ms), Duration: 8 * ms, Err: record.ErrExpect},
		},
		Iterations: []record.IterationRecord{
			{Scenario: "shop", UserID: 1, Start: t0, Duration: 200 * ms, Think: 50 * ms},
			{Scenario: "shop", UserID: 2, Start: t0.Add(1200 * ms), Duration: 1700 * ms, Think: 100 * ms, Err: record.ErrTimeout},
			{Scenario: "admin", UserID: 3, Start: t0.Add(1500 * ms), Duration: 10 * ms, Err: record.ErrExpect},
		},
		Health: []record.HealthPoint{{Second: 0, Goroutines: 12, CPU: 0.25}, {Second: 1, Goroutines: 31, CPU: -1}},
	}
	return FromRaw(raw)
}

// TestWriteJSONGolden compares what WriteJSON emits for a fixed report with a committed file. A
// failure means the emitted shape changed. If that was intended: update docs/result.md (and
// result_jp.md), raise JSONVersion when the change is not backward compatible for readers, add a
// fixture of the new version (see testdata/README.md), and run
//
//	go test ./report -run TestWriteJSONGolden -update
//
// so that the new file shows up in the review as a diff.
func TestWriteJSONGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := goldenReport().WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	const name = "golden-current.json"
	if *update {
		if err := os.WriteFile(filepath.Join("testdata", name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := testdataBytes(t, name); !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("WriteJSON output changed (JSONVersion = %d). Update docs/result.md and the fixtures, then run\n"+
			"go test ./report -run TestWriteJSONGolden -update\n--- got\n%s\n--- want\n%s", JSONVersion, buf.Bytes(), want)
	}

	// The golden file itself reads back as the same report.
	rep, err := ReadJSON(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep, goldenReport()) {
		t.Errorf("the golden JSON does not read back as the report it was written from")
	}
}

// TestFixtureVersionsCoverTheCurrentOne fails when JSONVersion is raised without a fixture of the
// new version, so every version that can be read has a stored file.
func TestFixtureVersionsCoverTheCurrentOne(t *testing.T) {
	seen := map[int]bool{}
	for _, f := range jsonFixtures {
		seen[f.version] = true
	}
	for v := 1; v <= JSONVersion; v++ {
		if !seen[v] {
			t.Errorf("no fixture of JSON version %d in testdata (see testdata/README.md)", v)
		}
	}
}
