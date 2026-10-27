package jotaro

import (
	"errors"
	"fmt"
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

// Stage は Stages の 1 段階。Arrival を For の間だけ続ける。
type Stage struct {
	Arrival Arrival
	For     time.Duration
}

// Stages は複数の段階を順に実行する。
// 各段階の Arrival は、その段階の開始を 0 として動く（Ramp の傾斜もそこから始まる）。
// 例えば、30 秒かけて 50/s まで上げ、60 秒保ち、10 秒で 0 まで下げる:
//
//	Stages(
//		Stage{Ramp(0, 50, time.Second, 30*time.Second), 30 * time.Second},
//		Stage{Rate(50, time.Second), 60 * time.Second},
//		Stage{Ramp(50, 0, time.Second, 10*time.Second), 10 * time.Second},
//	)
//
// 全段階が終わると到着は止まる。実行全体の長さは Plan.Duration で決まるので、
// 各段階の For の合計に合わせる。
func Stages(stages ...Stage) Arrival {
	s := stagedArrival{stages: stages}
	var start time.Duration
	for _, st := range stages {
		s.starts = append(s.starts, start)
		s.counts = append(s.counts, st.count())
		start += st.For
	}
	return s
}

type stagedArrival struct {
	stages []Stage
	starts []time.Duration // 各段階の開始時刻
	counts []int           // 各段階の到着数
}

// count は段階の中の到着数（到着時刻が For より前のもの）。
func (st Stage) count() int {
	if st.Arrival == nil || st.For <= 0 || st.Arrival.validate() != nil {
		return 0
	}
	in := func(i int) bool {
		off, ok := st.Arrival.at(i)
		return ok && off < st.For
	}
	// at は単調に増えるので、in が偽になる最初の i を二分探索で求める。
	hi := 1
	for in(hi) && hi < 1<<40 {
		hi *= 2
	}
	lo := 0
	if !in(0) {
		return 0
	}
	for lo+1 < hi { // in(lo) は真、in(hi) は偽
		mid := lo + (hi-lo)/2
		if in(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo + 1
}

func (s stagedArrival) validate() error {
	if len(s.stages) == 0 {
		return errors.New("jotaro: Stages には 1 つ以上の段階が必要")
	}
	for i, st := range s.stages {
		switch {
		case st.Arrival == nil:
			return fmt.Errorf("jotaro: Stages の %d 番目の Arrival が未設定", i+1)
		case st.For <= 0:
			return fmt.Errorf("jotaro: Stages の %d 番目の For は正の値にする必要がある", i+1)
		}
		if err := st.Arrival.validate(); err != nil {
			return fmt.Errorf("stages[%d]: %w", i+1, err)
		}
	}
	return nil
}

func (s stagedArrival) at(i int) (time.Duration, bool) {
	for k, st := range s.stages {
		if i < s.counts[k] {
			off, ok := st.Arrival.at(i)
			return s.starts[k] + off, ok
		}
		i -= s.counts[k]
	}
	return 0, false
}
