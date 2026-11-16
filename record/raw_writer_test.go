package record

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A stream written by RawWriter reads back with the same records and the final run info.
func TestRawWriterRoundTrip(t *testing.T) {
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	req := Record{Scenario: "s", Step: "a", Method: "GET", URL: "http://x/", Code: 200, Start: start, Latency: 5 * time.Millisecond}
	step := StepRecord{Scenario: "s", Step: "a", Start: start, Duration: 6 * time.Millisecond}
	iter := IterationRecord{Scenario: "s", Start: start, Duration: 7 * time.Millisecond}
	health := []HealthPoint{{Second: 0, Goroutines: 4, CPU: 0.5}}

	var buf bytes.Buffer
	rw := NewRawWriter(&buf, true, start)
	rw.ObserveIterationStart(start)
	rw.ObserveRequest(req)
	rw.ObserveStep(step)
	rw.ObserveIteration(iter)
	if err := rw.Close(3, 2*time.Second, health); err != nil {
		t.Fatal(err)
	}
	rw.ObserveRequest(req) // after Close: dropped, not written

	got, err := ReadRaw(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := &Raw{
		Open: true, Dropped: 3, StartedAt: start, Elapsed: 2 * time.Second,
		Requests: []Record{req}, Steps: []StepRecord{step}, Iterations: []IterationRecord{iter}, Health: health,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Without Close (for example the process died), what was written so far is still readable.
func TestRawWriterUnfinishedStreamIsReadable(t *testing.T) {
	var buf bytes.Buffer
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	rw := NewRawWriter(&buf, false, start)
	rw.ObserveRequest(Record{Scenario: "s", Start: start, Latency: time.Millisecond})
	rw.e.bw.Flush() // what a crash would leave in the file at best

	got, err := ReadRaw(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Requests) != 1 || !got.StartedAt.Equal(start) {
		t.Errorf("got %+v", got)
	}
}

type errWriter struct{ n int }

func (w *errWriter) Write(p []byte) (int, error) {
	w.n++
	return 0, errors.New("disk full")
}

// A write error is kept: later records are skipped and Close returns it.
func TestRawWriterKeepsFirstError(t *testing.T) {
	w := &errWriter{}
	rw := NewRawWriter(w, false, time.Now())
	// Enough lines to overflow the buffer, so the writer is hit before Close.
	for i := 0; i < 1000; i++ {
		rw.ObserveRequest(Record{Scenario: "s", URL: strings.Repeat("x", 100)})
	}
	if err := rw.Close(0, time.Second, nil); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Close error = %v, want disk full", err)
	}
	if w.n != 1 {
		t.Errorf("underlying writes after the first failure: %d, want 1", w.n)
	}
}
