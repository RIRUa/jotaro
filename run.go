package jotaro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// Defaults applied when the matching Plan field is 0.
const (
	// defaultTimeout bounds one request (Plan.Timeout).
	defaultTimeout = 30 * time.Second
	// defaultGrace is how long running iterations may outlive Plan.Duration (Plan.Grace).
	defaultGrace = 30 * time.Second

	// defaultMaxUsers caps concurrency in the open model (Plan.MaxUsers).
	defaultMaxUsers = 1000

	// defaultMaxLag is how late an arrival may start before it is dropped (Plan.MaxLag).
	defaultMaxLag = time.Second

	// maxConcurrencyLimit is the hard cap on Plan.Users and Plan.MaxUsers. A single load
	// generator cannot sensibly drive more goroutines and sockets than this; Run rejects
	// larger values so a typo such as 1000000 does not exhaust the machine.
	maxConcurrencyLimit = 100_000
)

// Plan describes how load is applied.
//
// Specify exactly one of Users (closed model) and Arrival (open model).
//
//   - Users: a fixed number of users repeat the scenario until Duration has passed.
//   - Arrival: scenarios start at a fixed rate, regardless of how long a scenario takes.
type Plan struct {
	// Scenario is the flow every virtual user runs.
	Scenario *Scenario
	// BaseURL is prepended to the relative paths of requests (for example "http://localhost:8080").
	BaseURL string

	// Users is the number of virtual users running at the same time (at most 100000).
	Users int
	// Arrival is how scenarios arrive (e.g. Rate(10, time.Second)). It cannot be set together with Users.
	Arrival Arrival
	// MaxUsers is the cap on concurrency with Arrival (at most 100000). 0 means 1000.
	// Arrivals that come in while the cap is reached are not run and are counted as dropped.
	MaxUsers int
	// After Duration has passed, no new iterations are started.
	Duration time.Duration

	// MaxLag is how far behind its scheduled time an arrival may start with Arrival. An arrival that is
	// later than this (the load generator stalled) is dropped and counted in Report.Dropped, instead of
	// being started together with the backlog in a burst that would stall it further. 0 means 1 second;
	// a negative value starts every late arrival.
	MaxLag time.Duration

	// Timeout is the limit for a single request. 0 means 30 seconds.
	Timeout time.Duration
	// Grace is how long to wait for running iterations to finish after Duration.
	// Those that exceed it are aborted and recorded as record.ErrCanceled. 0 means 30 seconds.
	Grace time.Duration

	// MaxBodyBytes is the largest response body that is read into memory. A larger body fails the
	// request with ErrBodyTooLarge. 0 means 10 MiB; a negative value means no limit.
	MaxBodyBytes int64

	// Warn receives non-fatal warnings found before the run starts (for example, a concurrency
	// that exceeds the open file limit). nil means the warning is written to standard error.
	Warn func(msg string)

	// If Raw is not nil, the raw data is written as JSON Lines when Run finishes (record.WriteRaw).
	// Setting Raw keeps every record in memory until the run ends (about 240 MB per million requests),
	// so use it for short runs only.
	// Even if writing fails, the aggregated Report is returned (together with an error).
	Raw io.Writer
}

// validate checks that the Plan is runnable: a scenario is set, exactly one of Users and Arrival
// is given, and Duration is positive. Run calls it first, so a bad Plan fails before any load is sent.
func (p Plan) validate() error {
	switch {
	case p.Scenario == nil || p.Scenario.Fn == nil:
		return errors.New("jotaro: Plan.Scenario is not set")
	case p.Users > 0 && p.Arrival != nil:
		return errors.New("jotaro: Plan.Users and Plan.Arrival cannot be set together")
	case p.Users <= 0 && p.Arrival == nil:
		return errors.New("jotaro: Plan.Users must be at least 1 (or set Plan.Arrival)")
	case p.MaxUsers < 0:
		return errors.New("jotaro: Plan.MaxUsers must be at least 0")
	case p.Users > maxConcurrencyLimit:
		return fmt.Errorf("jotaro: Plan.Users must be at most %d", maxConcurrencyLimit)
	case p.MaxUsers > maxConcurrencyLimit:
		return fmt.Errorf("jotaro: Plan.MaxUsers must be at most %d", maxConcurrencyLimit)
	case p.Duration <= 0:
		return errors.New("jotaro: Plan.Duration must be positive")
	}
	if p.Arrival != nil {
		return p.Arrival.validate()
	}
	return nil
}

// maxConcurrency is the upper limit of iterations that can run at the same time.
//
// It sizes the connection pool in Run and the slot channel in runOpen.
func (p Plan) maxConcurrency() int {
	if p.Arrival == nil {
		return p.Users
	}
	if p.MaxUsers > 0 {
		return p.MaxUsers
	}
	return defaultMaxUsers
}

// Run applies load according to the Plan, then aggregates and returns the result.
//
// Flow of a run:
//
//  1. validate the Plan, and clone the default Transport so connections are not shared with other code
//  2. start the health sampler, then run the closed (Users) or open (Arrival) loop until Duration ends
//  3. wait up to Grace for running iterations, then cancel them
//  4. build the report and write the raw data if Plan.Raw is set. Without Plan.Raw, records are folded into
//     histograms as they arrive (percentiles within 1% relative error, memory independent of the run length).
//     With Plan.Raw, every record is kept and the percentiles are exact
//
// When ctx is done, everything stops at once, including running iterations,
// and the results so far are returned. Report.Elapsed is the real time until all users have stopped.
func Run(ctx context.Context, p Plan) (*report.Report, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	p.checkFDLimit()
	timeout := cmpOr(p.Timeout, defaultTimeout)
	grace := cmpOr(p.Grace, defaultGrace)

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = p.maxConcurrency() * 2
	tr.MaxIdleConnsPerHost = p.maxConcurrency()
	defer tr.CloseIdleConnections()

	// stopCtx: whether new iterations may be started.
	// runCtx:  whether running iterations may continue. It is cancelled at Duration + Grace.
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	// Stop by cancellation, not by deadline. With DeadlineExceeded it could not be
	// told apart from a request's own timeout (record.ErrTimeout).
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer time.AfterFunc(p.Duration+grace, cancel).Stop()

	start := time.Now()
	// Raw data needs every record, so keep them in that case and aggregate exactly at the end.
	// Otherwise fold each record into histograms as it arrives, so memory does not grow with the run.
	var (
		col *record.Collector
		agg *report.Aggregator
	)
	if p.Raw != nil {
		col = record.NewCollector()
	} else {
		agg = report.NewAggregator(start)
		col = record.NewStreamingCollector(agg)
	}
	r := &runner{p: p, tr: tr, timeout: timeout, col: col, stopCtx: stopCtx, runCtx: runCtx}
	health := startHealthSampler(report.SeriesInterval)
	if p.Arrival != nil {
		r.runOpen()
	} else {
		r.runClosed()
	}
	elapsed := time.Since(start)
	healthPoints := health.stop()

	if agg != nil {
		rep := agg.Report(elapsed)
		rep.Open = p.Arrival != nil
		rep.Dropped = r.col.Dropped()
		rep.Health = healthPoints
		return rep, nil
	}

	reqs, steps, iters := r.col.Take()
	raw := &record.Raw{
		Open:       p.Arrival != nil,
		Dropped:    r.col.Dropped(),
		StartedAt:  start,
		Elapsed:    elapsed,
		Requests:   reqs,
		Steps:      steps,
		Iterations: iters,
		Health:     healthPoints,
	}
	rep := report.FromRaw(raw)
	if p.Raw != nil {
		if err := record.WriteRaw(p.Raw, raw); err != nil {
			return rep, fmt.Errorf("jotaro: failed to write raw data: %w", err)
		}
	}
	return rep, nil
}

// runner is the state during one Run.
type runner struct {
	p       Plan
	tr      http.RoundTripper
	timeout time.Duration
	col     *record.Collector

	stopCtx context.Context
	runCtx  context.Context
}

// runClosed has Users users repeat the scenario until Duration has passed.
func (r *runner) runClosed() {
	var wg sync.WaitGroup
	for id := 0; id < r.p.Users; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; r.stopCtx.Err() == nil; iter++ {
				u, err := newUser(r.runCtx, r.p, id, iter, r.tr, r.timeout, r.col)
				if err != nil {
					return
				}
				_ = r.p.Scenario.run(u) // the error is kept in the IterationRecord
			}
		}()
	}
	wg.Wait()
}

// runOpen starts iterations at the arrival times of Arrival.
//
// Arrival times are measured from the start time, so the spacing does not
// drift even if the dispatcher falls behind. Arrivals that are late by up to Plan.MaxLag are started together,
// and the scheduled time is kept in Scheduled. Arrivals later than that, and arrivals that come in
// while concurrency is at the cap, are dropped.
func (r *runner) runOpen() {
	slots := make(chan struct{}, r.p.maxConcurrency())

	var wg sync.WaitGroup
	defer wg.Wait()

	t0 := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C

	for i := 0; r.stopCtx.Err() == nil; i++ {
		off, ok := r.p.Arrival.at(i)
		if !ok {
			return
		}
		at := t0.Add(off)
		if wait := time.Until(at); wait > 0 {
			timer.Reset(wait)
			select {
			case <-timer.C:
			case <-r.stopCtx.Done():
				return
			}
		}

		if r.tooLate(at) {
			r.col.AddDropped()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			r.col.AddDropped()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			// In the open model, each arrival is treated as a separate user.
			u, err := newUser(r.runCtx, r.p, i, 0, r.tr, r.timeout, r.col)
			if err != nil {
				return
			}
			u.scheduled = at
			_ = r.p.Scenario.run(u) // the error is kept in the IterationRecord
		}()
	}
}

// tooLate reports whether an arrival scheduled for at is past Plan.MaxLag now.
func (r *runner) tooLate(at time.Time) bool {
	maxLag := r.p.MaxLag
	if maxLag == 0 {
		maxLag = defaultMaxLag
	}
	return maxLag > 0 && time.Since(at) > maxLag
}

// newUser creates the user for one iteration. The cookie jar is new every time.
func newUser(ctx context.Context, p Plan, id, iter int, tr http.RoundTripper, timeout time.Duration, col *record.Collector) (*User, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &User{
		ID:        id,
		Iteration: iter,
		Client:    &http.Client{Transport: tr, Jar: jar, Timeout: timeout},
		ctx:       ctx,
		scenario:  p.Scenario.Name,
		baseURL:   p.BaseURL,
		maxBody:   p.MaxBodyBytes,
		col:       col,
	}, nil
}

// cmpOr returns d, or fallback when d is 0. It is like cmp.Or, but only for durations.
func cmpOr(d, fallback time.Duration) time.Duration {
	if d != 0 {
		return d
	}
	return fallback
}
