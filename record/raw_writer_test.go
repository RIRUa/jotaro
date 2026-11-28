package record

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"sync"
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

// syncBuffer is a bytes.Buffer that the flush timer and the test can use at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// waitFor polls cond until it holds or a generous deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Records reach the output on their own, without Close and without the buffer filling up.
func TestRawWriterFlushesPeriodically(t *testing.T) {
	var out syncBuffer
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	rw := newRawWriter(&out, false, start, 10*time.Millisecond)
	defer rw.Close(0, 0, nil)

	// The meta line is flushed even if no record follows.
	waitFor(t, "the meta line", func() bool { return bytes.Count(out.bytes(), []byte("\n")) == 1 })

	// A few records are far below the 4 KiB buffer.
	for i := 0; i < 3; i++ {
		rw.ObserveRequest(Record{Scenario: "s", Step: "a", Start: start, Latency: time.Millisecond})
	}
	waitFor(t, "the records", func() bool { return bytes.Count(out.bytes(), []byte("\n")) == 4 })
	got, err := ReadRaw(bytes.NewReader(out.bytes()))
	if err != nil || got.Truncated || len(got.Requests) != 3 {
		t.Errorf("raw = %+v, err = %v", got, err)
	}

	// It keeps flushing after the first time (the timer is re-armed by the next write).
	rw.ObserveStep(StepRecord{Scenario: "s", Step: "a", Start: start, Duration: time.Millisecond})
	waitFor(t, "a later record", func() bool { return bytes.Count(out.bytes(), []byte("\n")) == 5 })
}

// The case of the issue: a run killed after 100 records leaves a file ReadRaw can read.
func TestRawWriterKilledRunIsReadable(t *testing.T) {
	var out syncBuffer
	start := time.Now()
	rw := newRawWriter(&out, false, start, 10*time.Millisecond)
	for i := 0; i < 100; i++ {
		rw.ObserveRequest(Record{Scenario: "s", Step: "a", Method: "GET", URL: "http://x/y", Start: start, Latency: time.Millisecond})
	}
	// No Close: the process died here. After the next flush the file ends at a line boundary.
	waitFor(t, "all records on the output", func() bool {
		b := out.bytes()
		return bytes.Count(b, []byte("\n")) == 101 && b[len(b)-1] == '\n'
	})
	got, err := ReadRaw(bytes.NewReader(out.bytes()))
	if err != nil || got.Truncated || len(got.Requests) != 100 {
		t.Errorf("requests = %d, truncated = %v, err = %v", len(got.Requests), got.Truncated, err)
	}
	// What a kill in the middle of a buffer-sized write leaves is a cut last line: still readable.
	b := out.bytes()
	got, err = ReadRaw(bytes.NewReader(b[:len(b)-10]))
	if err != nil || !got.Truncated || len(got.Requests) != 99 {
		t.Errorf("cut stream: requests = %d, truncated = %v, err = %v", len(got.Requests), got.Truncated, err)
	}
	rw.Close(0, time.Second, nil)
}

// After Close nothing is flushed or written by the timer, and writing concurrently with it is safe (run with -race).
func TestRawWriterFlushWithConcurrentWrites(t *testing.T) {
	var out syncBuffer
	start := time.Now()
	rw := newRawWriter(&out, false, start, time.Millisecond)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				rw.ObserveRequest(Record{Scenario: "s", Step: "a", Start: start, Latency: time.Millisecond})
				if i%50 == 0 {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	if err := rw.Close(0, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	n := len(out.bytes())
	time.Sleep(20 * time.Millisecond)
	if len(out.bytes()) != n {
		t.Error("output changed after Close")
	}
	got, err := ReadRaw(bytes.NewReader(out.bytes()))
	if err != nil || got.Truncated || len(got.Requests) != 800 {
		t.Errorf("requests = %d, truncated = %v, err = %v", len(got.Requests), got.Truncated, err)
	}
}

// A failing flush is remembered like any other write error.
func TestRawWriterFlushErrorIsKept(t *testing.T) {
	rw := newRawWriter(&errWriter{}, false, time.Now(), 5*time.Millisecond)
	waitFor(t, "the flush error", func() bool {
		rw.mu.Lock()
		defer rw.mu.Unlock()
		return rw.err != nil
	})
	if err := rw.Close(0, 0, nil); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Close error = %v, want disk full", err)
	}
}
