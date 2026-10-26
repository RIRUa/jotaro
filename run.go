package jotaro

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	defaultGrace   = 30 * time.Second
)

// Plan は負荷のかけ方。
//
// 現状は Users（固定同時ユーザー数）だけに対応している。
// 各ユーザーは Duration が過ぎるまでシナリオを繰り返す。
type Plan struct {
	Scenario *Scenario
	BaseURL  string

	// Users は同時に動かす仮想ユーザー数。
	Users int
	// Duration を過ぎると、新しいイテレーションを始めなくなる。
	Duration time.Duration

	// Timeout は 1 リクエストあたりの上限。0 なら 30 秒。
	Timeout time.Duration
	// Grace は Duration 経過後、実行中のイテレーションの完了を待つ時間。
	// 超えたものは中断して ErrCanceled として記録する。0 なら 30 秒。
	Grace time.Duration
}

func (p Plan) validate() error {
	switch {
	case p.Scenario == nil || p.Scenario.Fn == nil:
		return errors.New("jotaro: Plan.Scenario が未設定")
	case p.Users <= 0:
		return errors.New("jotaro: Plan.Users は 1 以上にする必要がある")
	case p.Duration <= 0:
		return errors.New("jotaro: Plan.Duration は正の値にする必要がある")
	}
	return nil
}

// Run は Plan に従って負荷をかけ、結果を集計して返す。
//
// ctx が終わると、実行中のイテレーションも含めてすぐに止まり、
// そこまでの結果を返す。Report.Elapsed は、全ユーザーが止まるまでの実時間。
func Run(ctx context.Context, p Plan) (*Report, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	timeout := cmpOr(p.Timeout, defaultTimeout)
	grace := cmpOr(p.Grace, defaultGrace)

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = p.Users * 2
	tr.MaxIdleConnsPerHost = p.Users
	defer tr.CloseIdleConnections()

	// stopCtx: 新しいイテレーションを始めてよいか。
	// runCtx:  実行中のイテレーションを続けてよいか。Duration + Grace で中断する。
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	// 期限切れではなくキャンセルで止める。DeadlineExceeded だと、
	// リクエスト自体のタイムアウト(ErrTimeout)と区別できなくなる。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer time.AfterFunc(p.Duration+grace, cancel).Stop()

	col := NewCollector()
	start := time.Now()

	var wg sync.WaitGroup
	for id := 0; id < p.Users; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; stopCtx.Err() == nil; iter++ {
				u, err := newUser(runCtx, p, id, iter, tr, timeout, col)
				if err != nil {
					return
				}
				p.Scenario.run(u)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	reqs, steps, iters := col.Snapshot()
	return BuildReport(reqs, steps, iters, elapsed), nil
}

// newUser はイテレーション 1 回分のユーザーを作る。Cookie jar は毎回新しい。
func newUser(ctx context.Context, p Plan, id, iter int, tr http.RoundTripper, timeout time.Duration, col *Collector) (*User, error) {
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
