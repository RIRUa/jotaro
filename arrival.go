package jotaro

import (
	"errors"
	"time"
)

// Arrival はオープンモデルの到着のしかた。
// 到着の間隔はシナリオの所要時間と無関係に決まる。
type Arrival interface {
	// interval は到着の間隔。
	interval() time.Duration
	validate() error
}

// Rate は per ごとに n 回の割合でシナリオを開始する。
// 例えば Rate(10, time.Second) は毎秒 10 回。
func Rate(n int, per time.Duration) Arrival {
	return rate{n: n, per: per}
}

type rate struct {
	n   int
	per time.Duration
}

func (r rate) validate() error {
	switch {
	case r.n <= 0:
		return errors.New("jotaro: Rate の回数は 1 以上にする必要がある")
	case r.per <= 0:
		return errors.New("jotaro: Rate の期間は正の値にする必要がある")
	case r.per/time.Duration(r.n) <= 0:
		return errors.New("jotaro: Rate が高すぎて間隔が 1ns 未満になる")
	}
	return nil
}

func (r rate) interval() time.Duration {
	return r.per / time.Duration(r.n)
}
