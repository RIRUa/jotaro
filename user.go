package jotaro

import (
	"context"
	"net/http"
	"time"
)

// User はシナリオ 1 回分を実行する仮想ユーザー。
// ID は仮想ユーザーの番号で、イテレーションをまたいで変わらない。
// Client は Cookie jar をイテレーションごとに持つので、毎回新しいセッションになる。
type User struct {
	ID        int
	Iteration int
	Client    *http.Client

	ctx      context.Context
	scenario string
	baseURL  string
	col      *Collector

	think time.Duration
}

// Step はシナリオの 1 区間を実行し、その所要時間と成否を記録する。
// fn が返したエラーはそのまま返すので、続けるか止めるかは呼び出し側が決める。
func (u *User) Step(name string, fn func(s *Step) error) error {
	s := &Step{
		name: name,
		exec: &executor{
			ctx:     u.ctx,
			client:  u.Client,
			baseURL: u.baseURL,
			tag: Record{
				Scenario:  u.scenario,
				Step:      name,
				UserID:    u.ID,
				Iteration: u.Iteration,
			},
			sink: u.col.AddRequest,
		},
	}

	start := time.Now()
	err := fn(s)
	u.col.AddStep(StepRecord{
		Scenario:  u.scenario,
		Step:      name,
		UserID:    u.ID,
		Iteration: u.Iteration,
		Start:     start,
		Duration:  time.Since(start),
		Err:       classify(err),
	})
	return err
}

// Think は d だけ待つ。待った時間はステップにも Active にも含まれない。
// ctx が終わったら途中で戻る。
func (u *User) Think(d time.Duration) {
	if d <= 0 {
		return
	}
	start := time.Now()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-u.ctx.Done():
	}
	u.think += time.Since(start)
}
