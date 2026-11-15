package jotaro

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
)

// progressInterval is how often a progress line is written. It is a variable so tests can shorten it.
var progressInterval = time.Second

// progress is a light-weight record.Observer that tracks just enough to print a progress line:
// the number of requests, the number of failed ones and a histogram of their latencies.
// It does not keep records, so it costs the same whether or not Plan.Raw is set.
type progress struct {
	mu     sync.Mutex
	count  int
	failed int
	lat    histogram.H
}

// ObserveRequest counts one finished request.
func (p *progress) ObserveRequest(r record.Record) {
	p.mu.Lock()
	p.count++
	if r.Err != record.ErrNone {
		p.failed++
	}
	p.lat.Add(r.Latency)
	p.mu.Unlock()
}

func (p *progress) ObserveStep(record.StepRecord)           {}
func (p *progress) ObserveIteration(record.IterationRecord) {}
func (p *progress) ObserveIterationStart(time.Time)         {}

// progressStats is the state of a run at one moment.
type progressStats struct {
	count, failed int
	p95           time.Duration
}

// stats returns the totals so far.
func (p *progress) stats() progressStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := progressStats{count: p.count, failed: p.failed}
	if p.count > 0 {
		s.p95 = p.lat.Percentile(95)
	}
	return s
}

// formatProgress renders one progress line, such as "12s/60s rps=480 p95=38ms err=0.2%".
//
// rps is the throughput of the last interval (prev to cur over window), while p95 and err cover the whole run
// so far, so they converge to the values of the final report.
func formatProgress(elapsed, total time.Duration, prev, cur progressStats, window time.Duration) string {
	rps := 0.0
	if window > 0 {
		rps = float64(cur.count-prev.count) / window.Seconds()
	}
	errRate := 0.0
	if cur.count > 0 {
		errRate = float64(cur.failed) / float64(cur.count) * 100
	}
	return fmt.Sprintf("%ds/%ds rps=%.0f p95=%s err=%.1f%%",
		int(elapsed.Round(time.Second)/time.Second), int(total.Round(time.Second)/time.Second),
		rps, formatProgressDuration(cur.p95), errRate)
}

// formatProgressDuration prints d with a precision that keeps the line short.
func formatProgressDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	}
	return d.Round(time.Microsecond).String()
}

// reportProgress writes one line per progressInterval to w until stop is closed, and returns once it has stopped.
// The returned function stops it and waits, so nothing is written to w after Run returns.
// Errors from w are ignored: losing a progress line must not fail the run.
func reportProgress(w io.Writer, p *progress, start time.Time, total time.Duration) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		var prev progressStats
		last := start
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				cur := p.stats()
				fmt.Fprintln(w, formatProgress(now.Sub(start), total, prev, cur, now.Sub(last)))
				prev, last = cur, now
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// fanout passes every record to all of its observers.
type fanout []record.Observer

func (f fanout) ObserveRequest(r record.Record) {
	for _, o := range f {
		o.ObserveRequest(r)
	}
}

func (f fanout) ObserveStep(r record.StepRecord) {
	for _, o := range f {
		o.ObserveStep(r)
	}
}

func (f fanout) ObserveIteration(r record.IterationRecord) {
	for _, o := range f {
		o.ObserveIteration(r)
	}
}

func (f fanout) ObserveIterationStart(t time.Time) {
	for _, o := range f {
		o.ObserveIterationStart(t)
	}
}

// keeper is an Observer that stores records in a Collector, so that Plan.Raw and Plan.Progress can be used together.
type keeper struct{ col *record.Collector }

func (k keeper) ObserveRequest(r record.Record)            { k.col.AddRequest(r) }
func (k keeper) ObserveStep(r record.StepRecord)           { k.col.AddStep(r) }
func (k keeper) ObserveIteration(r record.IterationRecord) { k.col.AddIteration(r) }
func (keeper) ObserveIterationStart(time.Time)             {}
