package jotaro

import (
	"context"
	"fmt"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// Scenario は仮想ユーザーが 1 回実行する流れ。
type Scenario struct {
	Name string
	Fn   func(ctx context.Context, u *User) error
}

// NewScenario はシナリオを作る。
func NewScenario(name string, fn func(ctx context.Context, u *User) error) *Scenario {
	return &Scenario{Name: name, Fn: fn}
}

// run はシナリオを 1 回実行して record.IterationRecord を残す。
// Fn が panic しても、そのイテレーションの失敗として扱い、全体は止めない。
func (sc *Scenario) run(u *User) (err error) {
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("jotaro: シナリオが panic した: %v", p)
		}
		u.col.AddIteration(record.IterationRecord{
			Scenario:  sc.Name,
			UserID:    u.ID,
			Iteration: u.Iteration,
			Start:     start,
			Duration:  time.Since(start),
			Think:     u.think,
			Err:       classify(err),
		})
	}()
	return sc.Fn(u.ctx, u)
}
