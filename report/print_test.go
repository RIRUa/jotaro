package report

import (
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func TestPrintWarnsOnDroppedArrivals(t *testing.T) {
	rep := &Report{Open: true, Dropped: 3}
	var b strings.Builder
	rep.Print(&b)
	if !strings.Contains(b.String(), "3 arrivals were dropped") {
		t.Errorf("missing drop warning:\n%s", b.String())
	}

	rep.Dropped = 0
	b.Reset()
	rep.Print(&b)
	if strings.Contains(b.String(), "arrivals were dropped") {
		t.Errorf("unexpected drop warning:\n%s", b.String())
	}
}

// mismatchReport is a run of one step that fails by expect_mismatch alone: every request got a 200 and no
// request failed, so the only trace of the failures is StepStats.Errors.
func mismatchReport() *Report {
	var reqs []record.Record
	var steps []record.StepRecord
	for i := range 3 {
		reqs = append(reqs, record.Record{Scenario: "s", Step: "order", Code: 200, Latency: 10 * time.Millisecond})
		st := record.StepRecord{Scenario: "s", Step: "order", Duration: 10 * time.Millisecond}
		if i > 0 {
			st.Err = record.ErrExpect
		}
		steps = append(steps, st)
	}
	return BuildReport(reqs, steps, nil, time.Second)
}

// stepRow finds the table whose header line starts with the words of header and returns the header cells and the
// cells of the row of the step name, each split on runs of spaces.
func stepRow(t *testing.T, out, header, name string) (cols, row []string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(strings.Join(strings.Fields(l), " "), header) {
			continue
		}
		for _, r := range lines[i+1:] {
			if f := strings.Fields(r); len(f) > 0 && f[0] == name {
				return strings.Fields(l), f
			}
		}
	}
	t.Fatalf("no %q row under %q in:\n%s", name, header, out)
	return nil, nil
}

func TestPrintShowsStepFailureReasons(t *testing.T) {
	var b strings.Builder
	mismatchReport().Print(&b)
	out := b.String()

	// "steps (duration)": fail=2 is followed by its reason.
	cols, row := stepRow(t, out, "step count fail failures", "order")
	if cols[3] != "failures" || row[1] != "3" || row[2] != "2" || row[3] != "expect_mismatch=2" {
		t.Errorf("want count=3 fail=2 failures=expect_mismatch=2, got %v / %v\n%s", cols, row, out)
	}
	// "steps (requests)": the request-level column is named for what it counts and stays empty.
	cols, row = stepRow(t, out, "step reqs status", "order")
	if !strings.Contains(strings.Join(cols, " "), "net errors") || row[2] != "2xx=3" || row[3] != "-" {
		t.Errorf("want 2xx=3 and no net errors, got %v / %v", cols, row)
	}
}

func TestPrintStepWithoutFailuresShowsDash(t *testing.T) {
	var b strings.Builder
	rep := BuildReport(
		[]record.Record{{Scenario: "s", Step: "ok", Code: 200, Latency: time.Millisecond}},
		[]record.StepRecord{{Scenario: "s", Step: "ok", Duration: time.Millisecond}}, nil, time.Second)
	rep.Print(&b)
	_, row := stepRow(t, b.String(), "step count fail failures", "ok")
	if row[2] != "0" || row[3] != "-" {
		t.Errorf("want fail=0 failures=-, got %v", row)
	}
}
