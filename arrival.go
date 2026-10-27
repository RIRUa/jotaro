package jotaro

import (
	"errors"
	"math"
	"time"
)

// Arrival はオープンモデルの到着のしかた。
// 到着の時刻はシナリオの所要時間と無関係に決まる。
type Arrival interface {
	// at は i 番目（0 始まり）の到着の、開始からの経過時間。
	// i 番目以降に到着が無い（レートが 0 になった）ときは false。
	at(i int) (time.Duration, bool)
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

func (r rate) at(i int) (time.Duration, bool) {
	return time.Duration(i) * (r.per / time.Duration(r.n)), true
}

// Ramp は到着のレートを from から to へ、over の間に直線的に変える。
// レートは per ごとの回数で、over が過ぎたあとは to のまま続ける（to が 0 なら到着はそこで止まる）。
// 例えば Ramp(0, 50, time.Second, 30*time.Second) は、毎秒 0 回から 50 回へ 30 秒かけて増やす。
//
// 実行全体の長さは Plan.Duration で決まる。over が Duration より長いと、to には届かない。
func Ramp(from, to int, per, over time.Duration) Arrival {
	return ramp{from: from, to: to, per: per, over: over}
}

type ramp struct {
	from, to  int
	per, over time.Duration
}

func (r ramp) validate() error {
	switch {
	case r.from < 0 || r.to < 0:
		return errors.New("jotaro: Ramp の回数は 0 以上にする必要がある")
	case r.from == 0 && r.to == 0:
		return errors.New("jotaro: Ramp の回数が両方 0 では到着しない")
	case r.per <= 0:
		return errors.New("jotaro: Ramp の期間は正の値にする必要がある")
	case r.over <= 0:
		return errors.New("jotaro: Ramp の傾斜の時間は正の値にする必要がある")
	case r.per/time.Duration(max(r.from, r.to)) <= 0:
		return errors.New("jotaro: Ramp が高すぎて間隔が 1ns 未満になる")
	}
	return nil
}

// at は、開始から t 秒までの累積到着数 N(t) が i になる t を求める。
//
//	レート r(t) = from + (to-from)·t/over   (0 ≤ t ≤ over、単位は 1/秒)
//	N(t) = from·t + (to-from)·t²/(2·over)
//
// over を過ぎると、レートは to で一定になる。
func (r ramp) at(i int) (time.Duration, bool) {
	per, over := r.per.Seconds(), r.over.Seconds()
	from, to := float64(r.from)/per, float64(r.to)/per
	n := float64(i)

	a := (to - from) / (2 * over)     // N(t) = a·t² + from·t
	inRamp := a*over*over + from*over // N(over)
	var t float64
	switch {
	case n <= inRamp:
		if math.Abs(a) < 1e-12 {
			t = n / from
		} else {
			t = (-from + math.Sqrt(from*from+4*a*n)) / (2 * a)
		}
	case to == 0:
		return 0, false
	default:
		t = over + (n-inRamp)/to
	}
	return time.Duration(t * float64(time.Second)), true
}
