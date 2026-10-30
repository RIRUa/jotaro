package report

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func TestHealthJSONRoundTrip(t *testing.T) {
	rep := BuildReport([]record.Record{{Latency: time.Millisecond}}, nil, nil, time.Second)
	rep.Health = []record.HealthPoint{{Second: 0, Goroutines: 12, CPU: 0.25}, {Second: 1, Goroutines: 40, CPU: -1}}

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Health, rep.Health) {
		t.Errorf("health differs:\n got %+v\nwant %+v", got.Health, rep.Health)
	}
}

func TestReadJSONWithoutHealth(t *testing.T) {
	rep, err := ReadJSON(strings.NewReader(`{"version":3,"requests":{},"scenarios":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Health != nil {
		t.Errorf("Health = %+v; JSON without it is read as nil", rep.Health)
	}
}

func TestMergeHealth(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &Report{StartedAt: t0, Elapsed: 2 * time.Second, Health: []record.HealthPoint{{Second: 0, Goroutines: 10, CPU: 0.2}, {Second: 1, Goroutines: 12, CPU: 0.9}}}
	b := &Report{StartedAt: t0.Add(time.Second), Elapsed: 2 * time.Second, Health: []record.HealthPoint{{Second: 0, Goroutines: 5, CPU: 0.5}, {Second: 1, Goroutines: 6, CPU: -1}}}

	got, err := MergeReports(a, b)
	if err != nil {
		t.Fatal(err)
	}
	// b started 1 second later, so b's second 0 overlaps a's second 1.
	want := []record.HealthPoint{{Second: 0, Goroutines: 10, CPU: 0.2}, {Second: 1, Goroutines: 12 + 5, CPU: 0.9}, {Second: 2, Goroutines: 6, CPU: -1}}
	if !reflect.DeepEqual(got.Health, want) {
		t.Errorf("Health = %+v\nwant     %+v", got.Health, want)
	}
}

func healthReport(pts ...record.HealthPoint) *Report {
	rep := BuildReport([]record.Record{{Code: 200, Latency: time.Millisecond}}, nil, nil, time.Second)
	rep.Health = pts
	return rep
}

func TestSummarizeHealth(t *testing.T) {
	if _, ok := summarizeHealth(nil); ok {
		t.Error("ok despite no points")
	}
	h, _ := summarizeHealth([]record.HealthPoint{{Second: 0, Goroutines: 10, CPU: 0.2}, {Second: 1, Goroutines: 30, CPU: 0.6}, {Second: 2, Goroutines: 20, CPU: -1}})
	if !h.HasCPU || h.MaxCPU != 0.6 || h.MaxGoroutines != 30 || h.Hot() {
		t.Errorf("h = %+v", h)
	}
	if got := h.AvgCPU; got < 0.399 || got > 0.401 { // -1 (unknown) is left out of the average
		t.Errorf("AvgCPU = %v, want 0.4", got)
	}
	if h, _ := summarizeHealth([]record.HealthPoint{{Second: 0, Goroutines: 1, CPU: 0.95}}); !h.Hot() {
		t.Errorf("0.95 should be suspected saturated: %+v", h)
	}
	if h, _ := summarizeHealth([]record.HealthPoint{{Second: 0, Goroutines: 1, CPU: -1}}); h.HasCPU || h.Hot() || !strings.Contains(h.String(), "cpu n/a") {
		t.Errorf("CPU unknown: %+v %q", h, h)
	}
}

func TestPrintHealth(t *testing.T) {
	var buf bytes.Buffer
	healthReport(record.HealthPoint{Second: 0, Goroutines: 10, CPU: 0.2}, record.HealthPoint{Second: 1, Goroutines: 30, CPU: 0.5}).Print(&buf)
	if out := buf.String(); !strings.Contains(out, "loadgen:    cpu max 50.0% (avg 35.0%), goroutines max 30") || strings.Contains(out, "warning:") {
		t.Errorf("output:\n%s", out)
	}

	buf.Reset()
	healthReport(record.HealthPoint{Second: 0, Goroutines: 10, CPU: 0.97}).Print(&buf)
	if out := buf.String(); !strings.Contains(out, "warning:    load generator CPU reached 97%") {
		t.Errorf("no warning:\n%s", out)
	}

	buf.Reset()
	healthReport().Print(&buf)
	if strings.Contains(buf.String(), "loadgen") {
		t.Error("not shown when there are no records")
	}
}

func TestWriteHTMLHealth(t *testing.T) {
	var buf bytes.Buffer
	if err := healthReport(record.HealthPoint{Second: 0, Goroutines: 10, CPU: 0.2}, record.HealthPoint{Second: 1, Goroutines: 30, CPU: 0.95}).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"load generator goroutines", "load generator CPU", "load generator</div>", "cpu max 95.0%", "⚠ load generator CPU reached 95%"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from output", want)
		}
	}

	// In an environment where CPU cannot be obtained, the CPU chart is not shown.
	buf.Reset()
	if err := healthReport(record.HealthPoint{Second: 0, Goroutines: 10, CPU: -1}).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "load generator goroutines") || strings.Contains(out, "load generator CPU (") {
		t.Error("only the goroutine chart when CPU is unknown")
	}

	buf.Reset()
	if err := healthReport().WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "load generator") {
		t.Error("not shown when there are no records")
	}
}
