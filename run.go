package jotaro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
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
)

// Plan describes how load is applied.
//
// Specify exactly one of Users, UserStages (closed model) and Arrival (open model).
//
//   - Users: a fixed number of users repeat the scenario until Duration has passed.
//   - UserStages: like Users, but the number of users changes over time (ramp up, hold, ramp down).
//   - Arrival: scenarios start at a fixed rate, regardless of how long a scenario takes.
type Plan struct {
	// Scenario is the flow every virtual user runs. Exactly one of Scenario and Scenarios must be set.
	Scenario *Scenario
	// Scenarios is a weighted mix of flows, for example 70% browse and 30% checkout. It cannot be set together
	// with Scenario. Every iteration (closed model) or arrival (open model) picks one scenario at random with
	// probability Weight divided by the sum of the weights. The report and thresholds already separate the
	// results by scenario name, so the scenarios of a mix need distinct names.
	Scenarios []Weighted
	// BaseURL is prepended to the relative paths of requests (for example "http://localhost:8080").
	BaseURL string

	// Users is the number of virtual users running at the same time.
	Users int
	// UserStages changes the number of users over time in the closed model. It cannot be set together with Users or Arrival.
	//
	// Every user keeps its ID (User.ID counts from 0) and its iteration count for the whole run, so a user
	// that is stopped by a smaller target and started again by a larger one carries on where it left off.
	// When the target goes down, the users with the highest IDs stop: each one finishes the iteration it is
	// running (it is not interrupted) and then waits. So the active-user count of the report (Series.Concurrency)
	// can be above the target for a while. After the last stage the number of users stays at its Target until Duration.
	UserStages []UserStage
	// StartUsers is how many users are already running when the first stage of UserStages begins, so that
	// the load does not have to ramp up from 0. 0 means none. It can only be set together with UserStages.
	StartUsers int
	// Arrival is how scenarios arrive (e.g. Rate(10, time.Second)). It cannot be set together with Users or UserStages.
	Arrival Arrival
	// MaxUsers is the cap on concurrency with Arrival. 0 means 1000.
	// Arrivals that come in while the cap is reached are not run and are counted as dropped.
	MaxUsers int
	// After Duration has passed, no new iterations are started.
	Duration time.Duration

	// Timeout is the limit for a single request. 0 means 30 seconds.
	Timeout time.Duration
	// Grace is how long to wait for running iterations to finish after Duration.
	// Those that exceed it are aborted and recorded as record.ErrCanceled. 0 means 30 seconds.
	Grace time.Duration

	// If Raw is not nil, the raw data is written to it as JSON Lines while the run is in progress, one line per
	// record as it arrives (record.RawWriter), so memory stays constant however many requests there are.
	// Even if writing fails, the aggregated Report is returned (together with an error).
	//
	// The Report that Run returns is aggregated incrementally, as without Raw, so its percentiles are within
	// 1% relative error. For exact percentiles, read the raw data back (record.ReadRaw, report.FromRaw,
	// or `jotaro report -raw`).
	Raw io.Writer

	// If Progress is not nil, one line is written to it every second while the run is in progress,
	// for example "12s/60s rps=480 p95=38ms err=0.2% 5xx=31.0% fail=31.2% dropped=7". Pass os.Stderr to
	// watch a long run. The fields are:
	//
	//   - elapsed/Duration, and rps, the throughput of the last second;
	//   - p95, the 95th percentile of the request latency over the run so far (an approximation within
	//     1% relative error);
	//   - err, the share of requests so far that ended in a transport-level error (a timeout, a refused
	//     connection and so on). It is the same as error_rate in the report. A request that got a response is
	//     never counted here, whatever its status, so a server that answers every request with 500 or with a
	//     body that fails an Expect check still shows err=0.0%;
	//   - 5xx, the share of requests so far that got a 5xx response (http_5xx_rate). A request that ended in
	//     a transport-level error is not counted here;
	//   - fail, the share of iterations so far that ended with an error of any kind, such as the error that
	//     a failed Expect check returns from the scenario (iteration_fail_rate). Only finished iterations are
	//     counted;
	//   - dropped, the number of arrivals dropped so far because of MaxUsers (open model, as dropped).
	//
	// 5xx, fail and dropped are left out while they are zero. So a line that shows only err=0.0% means no
	// 5xx response and no failed iteration yet, not only no transport error. Nothing is written after Run
	// returns, and write errors are ignored. It works together with Raw.
	Progress io.Writer

	// If MetricsAddr is not empty, Run serves the state of the run so far at http://<MetricsAddr>/metrics in the
	// Prometheus text format (for example ":9090"), so Grafana and similar tools can chart it while the run is in
	// progress. It has the request, response class and error counts, the throughput and the latency quantiles of the
	// whole run so far (within 1% relative error). The server stops when Run returns. If the address cannot be
	// listened on, Run fails before sending any request.
	MetricsAddr string

	// AbortOn stops the run early when any of its conditions holds, for example
	// {When: "error_rate>50%", For: 5 * time.Second}. A broken server is then not hit for the whole Duration.
	// See AbortRule for the syntax and how the conditions are evaluated.
	//
	// On abort no new iterations are started and the running ones get Grace, counted from the moment of the abort,
	// to finish (the rest is cancelled and recorded as record.ErrCanceled), exactly as at the end of Duration.
	// Run then returns the report of the run up to that point (and writes Plan.Raw) together with an error
	// that wraps ErrAborted. If Progress is set, a line saying why is written to it.
	AbortOn []AbortRule
}

// Weighted is a scenario with its share in Plan.Scenarios.
type Weighted struct {
	// Scenario is the flow to run.
	Scenario *Scenario
	// Weight is the relative share of this scenario. It must be positive; only the ratio between weights matters.
	Weight int
}

// pick returns the scenario for the next iteration or arrival.
//
// With Plan.Scenario it is that scenario; with Plan.Scenarios it is chosen at random in proportion to the weights.
func (p Plan) pick() *Scenario {
	if p.Scenarios == nil {
		return p.Scenario
	}
	total := 0
	for _, w := range p.Scenarios {
		total += w.Weight
	}
	n := rand.IntN(total)
	for _, w := range p.Scenarios {
		if n < w.Weight {
			return w.Scenario
		}
		n -= w.Weight
	}
	return p.Scenarios[len(p.Scenarios)-1].Scenario // unreachable: n < total
}

// validateScenarios checks that exactly one of Scenario and Scenarios is set and that each scenario is runnable.
func (p Plan) validateScenarios() error {
	if p.Scenarios == nil {
		if p.Scenario == nil || p.Scenario.Fn == nil {
			return errors.New("jotaro: Plan.Scenario is not set")
		}
		return nil
	}
	if p.Scenario != nil {
		return errors.New("jotaro: Plan.Scenario and Plan.Scenarios cannot be set together")
	}
	if len(p.Scenarios) == 0 {
		return errors.New("jotaro: Plan.Scenarios must not be empty")
	}
	names := map[string]bool{}
	for i, w := range p.Scenarios {
		switch {
		case w.Scenario == nil || w.Scenario.Fn == nil:
			return fmt.Errorf("jotaro: Plan.Scenarios[%d].Scenario is not set", i)
		case w.Weight <= 0:
			return fmt.Errorf("jotaro: Plan.Scenarios[%d].Weight must be positive", i)
		case names[w.Scenario.Name]:
			return fmt.Errorf("jotaro: Plan.Scenarios has two scenarios named %q", w.Scenario.Name)
		}
		names[w.Scenario.Name] = true
	}
	return nil
}

// validate checks that the Plan is runnable: a scenario is set, exactly one of Users, UserStages and Arrival
// is given, and Duration is positive. Run calls it first, so a bad Plan fails before any load is sent.
func (p Plan) validate() error {
	if err := p.validateScenarios(); err != nil {
		return err
	}
	switch {
	case p.modelCount() > 1:
		return errors.New("jotaro: Plan.Users, Plan.UserStages and Plan.Arrival cannot be set together")
	case p.StartUsers != 0 && p.UserStages == nil:
		return errors.New("jotaro: Plan.StartUsers can only be set together with Plan.UserStages")
	case p.modelCount() == 0:
		return errors.New("jotaro: Plan.Users must be at least 1 (or set Plan.UserStages or Plan.Arrival)")
	case p.MaxUsers < 0:
		return errors.New("jotaro: Plan.MaxUsers must be at least 0")
	case p.Duration <= 0:
		return errors.New("jotaro: Plan.Duration must be positive")
	}
	if _, err := parseAbortRules(p.AbortOn); err != nil {
		return err
	}
	switch {
	case p.UserStages != nil:
		return validateUserStages(p.StartUsers, p.UserStages)
	case p.Arrival != nil:
		return p.Arrival.validate()
	}
	return nil
}

// modelCount is how many of Users, UserStages and Arrival are set. It should be exactly 1.
func (p Plan) modelCount() int {
	n := 0
	if p.Users > 0 {
		n++
	}
	if p.UserStages != nil {
		n++
	}
	if p.Arrival != nil {
		n++
	}
	return n
}

// maxConcurrency is the upper limit of iterations that can run at the same time.
//
// It sizes the connection pool in Run and the slot channel in runOpen.
func (p Plan) maxConcurrency() int {
	if p.UserStages != nil {
		return maxStageTarget(p.StartUsers, p.UserStages)
	}
	if p.Arrival == nil {
		return p.Users
	}
	if p.MaxUsers > 0 {
		return p.MaxUsers
	}
	return defaultMaxUsers
}

// newTransport returns a private transport for a run. It clones http.DefaultTransport so the run keeps
// its settings (proxy, dialer and so on) but shares no connections with other code. When
// http.DefaultTransport has been replaced by something that is not an *http.Transport (a tracing or
// recording wrapper, for example), it falls back to a fresh transport with the standard library's defaults
// instead of panicking.
func newTransport() *http.Transport {
	if t, ok := http.DefaultTransport.(*http.Transport); ok && t != nil {
		return t.Clone()
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// Run applies load according to the Plan, then aggregates and returns the result.
//
// Flow of a run:
//
//  1. validate the Plan, and clone the default Transport so connections are not shared with other code
//  2. start the health sampler, then run the closed (Users or UserStages) or open (Arrival) loop until Duration ends
//  3. wait up to Grace for running iterations, then cancel them
//  4. build the report. Records are folded into histograms as they arrive (percentiles are approximate, within 1%
//     relative error, see report.Latency; memory independent of the run length). With Plan.Raw each record is
//     also written to it at once, and the stream is finished (health and final run info) when the run ends
//
// When ctx is done, everything stops at once, including running iterations,
// and the results so far are returned. Report.Elapsed is the real time until all users have stopped.
func Run(ctx context.Context, p Plan) (*report.Report, error) {
	return run(ctx, p, nil)
}

// run is Run with a seam for tests: a non-nil rt is used instead of the transport that Run prepares, so a
// test can serve a plan from memory (see fakeServer in the tests) without sockets, which lets it run
// inside a testing/synctest bubble on a fake clock. Run itself always passes nil.
func run(ctx context.Context, p Plan, rt http.RoundTripper) (*report.Report, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	timeout := cmpOr(p.Timeout, defaultTimeout)
	grace := cmpOr(p.Grace, defaultGrace)

	tr := rt
	if tr == nil {
		t := newTransport()
		trackDials(t)
		t.MaxIdleConns = p.maxConcurrency() * 2
		t.MaxIdleConnsPerHost = p.maxConcurrency()
		defer t.CloseIdleConnections()
		tr = t
	}

	// stopCtx: whether new iterations may be started.
	// runCtx:  whether running iterations may continue. It is cancelled at Duration + Grace.
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	// Stop by cancellation, not by deadline. With DeadlineExceeded it could not be
	// told apart from a request's own timeout (record.ErrTimeout).
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	graceTimer := time.AfterFunc(p.Duration+grace, cancel)
	defer graceTimer.Stop()
	abortRules, _ := parseAbortRules(p.AbortOn) // already checked by validate

	start := time.Now()
	// Every record is folded into an Aggregator as it arrives, so memory does not grow with the run.
	// The other observers see the same records: the raw data writer (Plan.Raw), which writes each record
	// to the output at once, and the progress counter (Plan.Progress). AbortOn reads the Aggregator.
	agg := report.NewAggregator(start)
	obs := fanout{agg}
	var rw *record.RawWriter
	if p.Raw != nil {
		rw = record.NewRawWriter(p.Raw, p.Arrival != nil, start)
		obs = append(obs, rw)
	}
	var prog *progress
	if p.Progress != nil {
		prog = &progress{}
		obs = append(obs, prog)
	}
	var col *record.Collector
	if len(obs) == 1 {
		col = record.NewStreamingCollector(agg)
	} else {
		col = record.NewStreamingCollector(obs)
	}
	if prog != nil {
		prog.dropped = col.Dropped
	}
	var live *report.Aggregator
	if len(abortRules) > 0 {
		live = agg
	}
	var stopProgress, stopAbort, stopMetrics func()
	if p.MetricsAddr != "" {
		var err error
		// The scrape needs only totals, so it skips the time series, which gets costly on a long run.
		// Dropped lives in the collector, not the Aggregator, so it is filled in here.
		stopMetrics, err = startMetricsServer(p.MetricsAddr, func() *report.Report {
			rep := agg.Snapshot(time.Since(start))
			rep.Dropped = col.Dropped()
			return rep
		})
		if err != nil {
			return nil, err
		}
		defer stopMetrics()
	}
	if prog != nil {
		stopProgress = reportProgress(p.Progress, prog, start, p.Duration)
	}
	// abortReason is written by the watcher goroutine and read after stopAbort has waited for it.
	var abortReason string
	if live != nil {
		stopAbort = watchAbort(abortRules, live, col, start, func(reason string) {
			abortReason = reason
			stop() // no new iterations; the running ones get Grace from now on
			graceTimer.Reset(grace)
		})
	}
	r := &runner{p: p, tr: tr, timeout: timeout, col: col, stopCtx: stopCtx, runCtx: runCtx}
	health := startHealthSampler(report.SeriesInterval)
	switch {
	case p.Arrival != nil:
		r.runOpen()
	case p.UserStages != nil:
		r.runStaged()
	default:
		r.runClosed()
	}
	elapsed := time.Since(start)
	if stopAbort != nil {
		stopAbort()
	}
	if stopProgress != nil {
		stopProgress()
	}
	r.reportPanic()
	var abortErr error
	if abortReason != "" {
		abortErr = fmt.Errorf("%w: %s", ErrAborted, abortReason)
		if p.Progress != nil {
			fmt.Fprintln(p.Progress, "aborted: "+abortReason)
		}
	}
	healthPoints := health.stop()

	rep := agg.Report(elapsed)
	rep.Open = p.Arrival != nil
	rep.Dropped = r.col.Dropped()
	rep.Health = healthPoints
	if rw != nil {
		if err := rw.Close(rep.Dropped, elapsed, healthPoints); err != nil {
			return rep, errors.Join(abortErr, fmt.Errorf("jotaro: failed to write raw data: %w", err))
		}
	}
	return rep, abortErr
}

// runner is the state during one Run.
type runner struct {
	p       Plan
	tr      http.RoundTripper
	timeout time.Duration
	col     *record.Collector

	stopCtx context.Context
	runCtx  context.Context

	// firstPanic is the first panic of a scenario in the run (see runScenario), set once.
	panicOnce  sync.Once
	firstPanic string
}

// runScenario runs the scenario once. The error is kept in the IterationRecord, so only a panic is
// noted here: the first one of the run is kept, with its stack, and Run writes it once after the run
// (see reportPanic). A panic is a bug in the test code, and without this a broken scenario would look
// like a failing server.
func (r *runner) runScenario(sc *Scenario, u *User) {
	var pe *panicError
	if err := sc.run(u); errors.As(err, &pe) {
		r.panicOnce.Do(func() {
			r.firstPanic = fmt.Sprintf("panic: scenario %q panicked (first panic of the run; the others are counted as the error kind \"panic\"): %v\n%s", sc.Name, pe.value, pe.stack)
		})
	}
}

// reportPanic writes the first panic of the run to Plan.Progress, or to os.Stderr when there is none.
// It is called after the workers have finished and the progress line has stopped, so the writer is not
// used concurrently.
func (r *runner) reportPanic() {
	if r.firstPanic == "" {
		return
	}
	var w io.Writer = os.Stderr
	if r.p.Progress != nil {
		w = r.p.Progress
	}
	fmt.Fprint(w, r.firstPanic)
}

// runClosed has Users users repeat the scenario until Duration has passed.
func (r *runner) runClosed() {
	var wg sync.WaitGroup
	for id := 0; id < r.p.Users; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; r.stopCtx.Err() == nil; iter++ {
				sc := r.p.pick()
				u := newUser(r.runCtx, r.p, sc, id, iter, r.tr, r.timeout, r.col)
				r.runScenario(sc, u) // the error is kept in the IterationRecord
			}
		}()
	}
	wg.Wait()
}

// runStaged is runClosed with a changing number of users (Plan.UserStages).
//
// All users are allocated up front and wait until the active count exceeds their ID. A controller moves the
// count at the times given by userChanges and wakes the users that just became active. A user that is
// above the count finishes its current iteration and waits again; it is never interrupted.
func (r *runner) runStaged() {
	n := maxStageTarget(r.p.StartUsers, r.p.UserStages)
	var active atomic.Int64
	active.Store(int64(r.p.StartUsers))
	// wake[id] has room for one pending signal, so a wake-up sent just before the user starts waiting is not lost.
	wake := make([]chan struct{}, n)
	for i := range wake {
		wake[i] = make(chan struct{}, 1)
	}

	var wg sync.WaitGroup
	for id := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; r.stopCtx.Err() == nil; {
				if int(active.Load()) <= id {
					select {
					case <-wake[id]:
					case <-r.stopCtx.Done():
					}
					continue // check the count and the context again
				}
				sc := r.p.pick()
				u := newUser(r.runCtx, r.p, sc, id, iter, r.tr, r.timeout, r.col)
				r.runScenario(sc, u) // the error is kept in the IterationRecord
				iter++
			}
		}()
	}

	t0 := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C
	prev := r.p.StartUsers
	for _, ch := range userChanges(r.p.StartUsers, r.p.UserStages) {
		if wait := time.Until(t0.Add(ch.at)); wait > 0 {
			timer.Reset(wait)
			select {
			case <-timer.C:
			case <-r.stopCtx.Done():
				wg.Wait()
				return
			}
		}
		active.Store(int64(ch.count))
		for id := prev; id < ch.count; id++ { // only an increase wakes users
			select {
			case wake[id] <- struct{}{}:
			default:
			}
		}
		prev = ch.count
	}
	wg.Wait()
}

// runOpen starts iterations at the arrival times of Arrival.
//
// Arrival times are measured from the start time, so the spacing does not
// drift even if the dispatcher falls behind. Late arrivals are started together, and the scheduled time is kept in Scheduled.
// Arrivals that come in while concurrency is at the cap are dropped.
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
			sc := r.p.pick()
			u := newUser(r.runCtx, r.p, sc, i, 0, r.tr, r.timeout, r.col)
			u.scheduled.set(at)
			r.runScenario(sc, u) // the error is kept in the IterationRecord
		}()
	}
}

// userBlock holds everything newUser creates, so that one allocation serves the user, its client and its
// cookie jar instead of one each.
type userBlock struct {
	user   User
	client http.Client
	jar    lazyJar
}

// newUser creates the user for one iteration. The cookie jar is new every time.
func newUser(ctx context.Context, p Plan, sc *Scenario, id, iter int, tr http.RoundTripper, timeout time.Duration, col *record.Collector) *User {
	b := &userBlock{}
	b.client = http.Client{Transport: tr, Jar: &b.jar, Timeout: timeout}
	b.user = User{
		ID:        id,
		Iteration: iter,
		Client:    &b.client,
		ctx:       ctx,
		scenario:  sc.Name,
		baseURL:   p.BaseURL,
		col:       col,
	}
	return &b.user
}

// lazyJar is a cookie jar that is created on the first Set-Cookie. Most requests of a load test never
// receive a cookie, and cookiejar.New allocates a jar and its map, so an iteration that sees no cookie
// does not pay for them. Until then it holds nothing and Cookies returns nil, as an empty jar does.
// It is safe for concurrent use, as http.CookieJar requires.
type lazyJar struct {
	jar atomic.Pointer[cookiejar.Jar]
}

func (j *lazyJar) Cookies(u *url.URL) []*http.Cookie {
	if jar := j.jar.Load(); jar != nil {
		return jar.Cookies(u)
	}
	return nil
}

func (j *lazyJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if len(cookies) == 0 {
		return
	}
	jar := j.jar.Load()
	if jar == nil {
		// cookiejar.New only returns an error for invalid options, and there are none.
		fresh, _ := cookiejar.New(nil)
		if j.jar.CompareAndSwap(nil, fresh) {
			jar = fresh
		} else {
			jar = j.jar.Load()
		}
	}
	jar.SetCookies(u, cookies)
}

// cmpOr returns d, or fallback when d is 0. It is like cmp.Or, but only for durations.
func cmpOr(d, fallback time.Duration) time.Duration {
	if d != 0 {
		return d
	}
	return fallback
}
