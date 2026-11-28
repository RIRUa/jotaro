package record

import (
	"io"
	"sync"
	"time"
)

// RawWriter writes raw data as JSON Lines while a run is in progress, instead of after it.
// Nothing is kept in memory except the write buffer, so the cost does not grow with the number of requests
// (WriteRaw needs a whole Raw, which holds every record).
//
// It implements Observer, so it can be given to NewStreamingCollector (or be one of several observers).
// Lines are written in the order the records arrive, so unlike WriteRaw's output requests, steps and iterations
// are interleaved. ReadRaw does not depend on the order.
//
// The stream starts with a meta line that has what is known at the start (Open and StartedAt). Close
// writes the health lines and a second meta line with the final Dropped and Elapsed; for fields in both,
// ReadRaw takes the last one.
//
// The buffer is flushed to w at most one second after a record is written (and at once in Close), so
// the stream can be followed live (tail -f) and, if the process dies before Close, at most about
// a second of records is lost. The last line may still be cut off; ReadRaw then returns what precedes it
// with Raw.Truncated set, and derives the run time from the records.
//
// A write error is remembered, later writes are skipped, and Close returns the error.
// All methods are safe for concurrent use.
type RawWriter struct {
	mu       sync.Mutex
	e        *rawEncoder
	open     bool
	start    time.Time
	err      error
	closed   bool
	interval time.Duration // longest time a written line waits in the buffer
	timer    *time.Timer   // armed while there is buffered data that no flush is scheduled for
	armed    bool
}

// rawFlushInterval is how long a line may stay in RawWriter's buffer before it is flushed to the output.
const rawFlushInterval = time.Second

// NewRawWriter starts a raw data stream on w and writes the first meta line.
// open is whether the run uses Arrival, and start is when the run started.
// The error of that first write is returned by Close, like any other.
func NewRawWriter(w io.Writer, open bool, start time.Time) *RawWriter {
	return newRawWriter(w, open, start, rawFlushInterval)
}

// newRawWriter is NewRawWriter with the flush interval as a parameter, so tests need not wait a second.
func newRawWriter(w io.Writer, open bool, start time.Time, interval time.Duration) *RawWriter {
	rw := &RawWriter{e: newRawEncoder(w), open: open, start: start, interval: interval}
	rw.err = rw.e.meta(open, 0, start, 0)
	rw.schedule()
	return rw
}

// schedule arranges a flush after rw.interval if none is pending. There is no goroutine or ticker
// while the writer is idle, so a RawWriter that is dropped without Close leaks nothing.
// rw.mu must be held.
func (rw *RawWriter) schedule() {
	if rw.armed || rw.closed || rw.err != nil {
		return
	}
	rw.armed = true
	if rw.timer == nil {
		rw.timer = time.AfterFunc(rw.interval, rw.flush)
	} else {
		rw.timer.Reset(rw.interval)
	}
}

// flush is the timer callback: it pushes the buffered lines to the output.
// Only whole lines are in the buffer, because every write holds rw.mu until its line is encoded.
func (rw *RawWriter) flush() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	rw.armed = false
	if rw.err != nil || rw.closed {
		return
	}
	rw.err = rw.e.bw.Flush()
}

// write runs f unless an earlier write failed or the stream is closed, and keeps its error.
func (rw *RawWriter) write(f func(*rawEncoder) error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.err != nil || rw.closed {
		return
	}
	rw.err = f(rw.e)
	rw.schedule()
}

// ObserveRequest writes one request line.
func (rw *RawWriter) ObserveRequest(r Record) {
	rw.write(func(e *rawEncoder) error { return e.request(r) })
}

// ObserveStep writes one step line.
func (rw *RawWriter) ObserveStep(s StepRecord) {
	rw.write(func(e *rawEncoder) error { return e.step(s) })
}

// ObserveIteration writes one iteration line.
func (rw *RawWriter) ObserveIteration(it IterationRecord) {
	rw.write(func(e *rawEncoder) error { return e.iteration(it) })
}

// ObserveIterationStart does nothing: the start time is already in the IterationRecord.
func (*RawWriter) ObserveIterationStart(time.Time) {}

// Close writes the health lines and the final meta line, flushes, and returns the first error of the stream.
// Call it once, after the run is over and no more records arrive. Records observed after Close are dropped.
func (rw *RawWriter) Close(dropped int, elapsed time.Duration, health []HealthPoint) error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.closed {
		return rw.err
	}
	rw.closed = true
	if rw.timer != nil {
		rw.timer.Stop()
	}
	if rw.err != nil {
		return rw.err
	}
	for _, h := range health {
		if rw.err = rw.e.health(h); rw.err != nil {
			return rw.err
		}
	}
	if rw.err = rw.e.meta(rw.open, dropped, rw.start, elapsed); rw.err != nil {
		return rw.err
	}
	rw.err = rw.e.bw.Flush()
	return rw.err
}
