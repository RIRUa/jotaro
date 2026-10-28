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

const (
	defaultTimeout = 30 * time.Second
	defaultGrace   = 30 * time.Second

	defaultMaxUsers = 1000
)

// Plan describes how load is applied.
//
// Specify exactly one of Users (closed model) and Arrival (open model).
//
//   - Users: a fixed number of users repeat the scenario until Duration has passed.
//   - Arrival: scenarios start at a fixed rate, regardless of how long a scenario takes.
type Plan struct {
	Scenario *Scenario
	BaseURL  string

	// Users is the number of virtual users running at the same time.
	Users int
	// Arrival is how scenarios arrive (e.g. Rate(10, time.Second)). It cannot be set together with Users.
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

	// If Raw is not nil, the raw data is written as JSON Lines when Run finishes (record.WriteRaw).
	// Even if writing fails, the aggregated Report is returned (together with an error).
	Raw io.Writer
}

func (p Plan) validate() error {
	switch {
	case p.Scenario == nil || p.Scenario.Fn == nil:
		return errors.New("jotaro: Plan.Scenario が未設定")
	case p.Users > 0 && p.Arrival != nil:
		return errors.New("jotaro: Plan.Users と Plan.Arrival は同時に指定できない")
	case p.Users <= 0 && p.Arrival == nil:
		return errors.New("jotaro: Plan.Users は 1 以上にする必要がある（または Plan.Arrival を指定する）")
	case p.MaxUsers < 0:
		return errors.New("jotaro: Plan.MaxUsers は 0 以上にする必要がある")
	case p.Duration <= 0:
		return errors.New("jotaro: Plan.Duration は正の値にする必要がある")
	}
	if p.Arrival != nil {
		return p.Arrival.validate()
	}
	return nil
}

// maxConcurrency は同時に動きうるイテレーション数の上限。
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
// When ctx is done, everything stops at once, including running iterations,
// and the results so far are returned. Report.Elapsed is the real time until all users have stopped.
func Run(ctx context.Context, p Plan) (*report.Report, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	timeout := cmpOr(p.Timeout, defaultTimeout)
	grace := cmpOr(p.Grace, defaultGrace)

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = p.maxConcurrency() * 2
	tr.MaxIdleConnsPerHost = p.maxConcurrency()
	defer tr.CloseIdleConnections()

	// stopCtx: 新しいイテレーションを始めてよいか。
	// runCtx:  実行中のイテレーションを続けてよいか。Duration + Grace で中断する。
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	// 期限切れではなくキャンセルで止める。DeadlineExceeded だと、
	// リクエスト自体のタイムアウト(record.ErrTimeout)と区別できなくなる。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer time.AfterFunc(p.Duration+grace, cancel).Stop()

	r := &runner{p: p, tr: tr, timeout: timeout, col: record.NewCollector(), stopCtx: stopCtx, runCtx: runCtx}
	start := time.Now()
	health := startHealthSampler(report.SeriesInterval)
	if p.Arrival != nil {
		r.runOpen()
	} else {
		r.runClosed()
	}
	elapsed := time.Since(start)
	healthPoints := health.stop()

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
			return rep, fmt.Errorf("jotaro: 生データの書き出しに失敗: %w", err)
		}
	}
	return rep, nil
}

// runner は 1 回の Run の間の状態。
type runner struct {
	p       Plan
	tr      http.RoundTripper
	timeout time.Duration
	col     *record.Collector

	stopCtx context.Context
	runCtx  context.Context
}

// runClosed は Users 個のユーザーが、Duration が過ぎるまでシナリオを繰り返す。
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
				_ = r.p.Scenario.run(u) // エラーは IterationRecord に残る
			}
		}()
	}
	wg.Wait()
}

// runOpen は Arrival の到着時刻にイテレーションを開始する。
//
// 到着時刻は開始時刻からの経過で決めるので、ディスパッチャが遅れても
// 間隔はずれない。遅れた分はまとめて起動し、予定時刻を Scheduled に残す。
// 同時実行数が上限に達しているときの到着はドロップする。
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
			// オープンモデルでは到着ごとに別のユーザーとして扱う。
			u, err := newUser(r.runCtx, r.p, i, 0, r.tr, r.timeout, r.col)
			if err != nil {
				return
			}
			u.scheduled = at
			_ = r.p.Scenario.run(u) // エラーは IterationRecord に残る
		}()
	}
}

// newUser はイテレーション 1 回分のユーザーを作る。Cookie jar は毎回新しい。
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
		col:       col,
	}, nil
}

func cmpOr(d, fallback time.Duration) time.Duration {
	if d != 0 {
		return d
	}
	return fallback
}
