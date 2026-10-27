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

// Plan は負荷のかけ方。
//
// Users（クローズド）か Arrival（オープン）のどちらか一方を指定する。
//
//   - Users: 固定数のユーザーが Duration が過ぎるまでシナリオを繰り返す。
//   - Arrival: シナリオの所要時間に関わらず、一定の割合でシナリオを開始する。
type Plan struct {
	Scenario *Scenario
	BaseURL  string

	// Users は同時に動かす仮想ユーザー数。
	Users int
	// Arrival は到着のしかた（例: Rate(10, time.Second)）。Users とは同時に指定できない。
	Arrival Arrival
	// MaxUsers は Arrival のときの同時実行数の上限。0 なら 1000。
	// 上限に達しているときに到着した分は、実行せずドロップとして数える。
	MaxUsers int
	// Duration を過ぎると、新しいイテレーションを始めなくなる。
	Duration time.Duration

	// Timeout は 1 リクエストあたりの上限。0 なら 30 秒。
	Timeout time.Duration
	// Grace は Duration 経過後、実行中のイテレーションの完了を待つ時間。
	// 超えたものは中断して record.ErrCanceled として記録する。0 なら 30 秒。
	Grace time.Duration

	// Raw が nil でなければ、Run が終わったときに生データを JSON Lines で書き出す（record.WriteRaw）。
	// 書き出しに失敗しても、集計した Report は返す（error も同時に返す）。
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

// Run は Plan に従って負荷をかけ、結果を集計して返す。
//
// ctx が終わると、実行中のイテレーションも含めてすぐに止まり、
// そこまでの結果を返す。Report.Elapsed は、全ユーザーが止まるまでの実時間。
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

	reqs, steps, iters := r.col.Snapshot()
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
				r.p.Scenario.run(u)
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
			r.p.Scenario.run(u)
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
