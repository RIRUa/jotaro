package jotaro

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
)

// progressInterval is how often a progress line is written.
const progressInterval = time.Second

// progress is a light-weight record.Observer that tracks just enough to print a progress line:
// the number of requests, the number of transport-level errors, the number of 5xx responses, the number of
// iterations and failed ones, and a histogram of the request latencies.
// It does not keep records, so it costs the same whether or not Plan.Raw is set.
type progress struct {
	mu          sync.Mutex
	count       int
	failed      int // requests that ended in a transport-level error (Record.Err is set)
	http5xx     int // requests that got a 5xx response (never overlaps with failed)
	iters       int
	itersFailed int // iterations that ended with an error of any kind, such as a failed Expect check
	lat         histogram.H

	// dropped returns the number of dropped arrivals so far (the open model). It is nil when there is no source.
	dropped func() int
}

// ObserveRequest counts one finished request.
func (p *progress) ObserveRequest(r record.Record) {
	p.mu.Lock()
	p.count++
	if r.Err != record.ErrNone {
		p.failed++
	} else if r.Code/100 == 5 { // the same class as report.statusClass, so 6xx-9xx are not counted
		p.http5xx++
	}
	p.lat.Add(r.Latency)
	p.mu.Unlock()
}

// ObserveIteration counts one finished iteration, and whether it failed.
func (p *progress) ObserveIteration(r record.IterationRecord) {
	p.mu.Lock()
	p.iters++
	if r.Err != record.ErrNone {
		p.itersFailed++
	}
	p.mu.Unlock()
}

func (p *progress) ObserveStep(record.StepRecord)   {}
func (p *progress) ObserveIterationStart(time.Time) {}

// progressStats is the state of a run at one moment.
type progressStats struct {
	count, failed, http5xx int
	iters, itersFailed     int
	dropped                int
	p95                    time.Duration
}

// stats returns the totals so far.
func (p *progress) stats() progressStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := progressStats{
		count: p.count, failed: p.failed, http5xx: p.http5xx,
		iters: p.iters, itersFailed: p.itersFailed,
	}
	if p.count > 0 {
		s.p95 = p.lat.Percentile(95)
	}
	if p.dropped != nil {
		s.dropped = p.dropped()
	}
	return s
}

// formatProgress renders one progress line, such as "12s/60s rps=480 p95=38ms err=0.2% 5xx=31.0% fail=31.2% dropped=7".
//
// rps is the throughput of the last interval (prev to cur over window), while the others cover the whole run
// so far, so they converge to the values of the final report:
//   - p95 is the 95th percentile of the request latency;
//   - err is the share of requests that ended in a transport-level error (error_rate);
//   - 5xx is the share of requests that got a 5xx response (http_5xx_rate);
//   - fail is the share of iterations that ended with an error, such as a failed Expect check (iteration_fail_rate);
//   - dropped is the number of arrivals dropped by the concurrency cap (dropped).
//
// 5xx, fail and dropped are left out while they are zero, to keep a healthy line short.
func formatProgress(elapsed, total time.Duration, prev, cur progressStats, window time.Duration) string {
	rps := 0.0
	if window > 0 {
		rps = float64(cur.count-prev.count) / window.Seconds()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%ds/%ds rps=%.0f p95=%s err=%.1f%%",
		int(elapsed.Round(time.Second)/time.Second), int(total.Round(time.Second)/time.Second),
		rps, formatProgressDuration(cur.p95), progressPercent(cur.failed, cur.count))
	if cur.http5xx > 0 {
		fmt.Fprintf(&b, " 5xx=%.1f%%", progressPercent(cur.http5xx, cur.count))
	}
	if cur.itersFailed > 0 {
		fmt.Fprintf(&b, " fail=%.1f%%", progressPercent(cur.itersFailed, cur.iters))
	}
	if cur.dropped > 0 {
		fmt.Fprintf(&b, " dropped=%d", cur.dropped)
	}
	return b.String()
}

// progressPercent is n out of total as a percentage, or 0 when total is 0.
func progressPercent(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
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
