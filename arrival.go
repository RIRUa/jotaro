package jotaro

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Arrival is how scenarios arrive in the open model.
// Arrival times are decided independently of how long a scenario takes.
type Arrival interface {
	// at is the time since the start of the i-th arrival (0-based).
	// It is false when there is no arrival from the i-th on (the rate has dropped to 0).
	at(i int) (time.Duration, bool)
	validate() error
}

// Rate starts scenarios at a rate of n per per.
// For example, Rate(10, time.Second) is 10 per second.
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
		return errors.New("jotaro: Rate count must be at least 1")
	case r.per <= 0:
		return errors.New("jotaro: Rate period must be positive")
	case r.per/time.Duration(r.n) <= 0:
		return errors.New("jotaro: Rate is so high that the interval is under 1ns")
	}
	return nil
}

func (r rate) at(i int) (time.Duration, bool) {
	return time.Duration(i) * (r.per / time.Duration(r.n)), true
}

// Ramp changes the arrival rate linearly from from to to over over.
// The rate is a count per per; after over has passed it stays at to (if to is 0, arrivals stop there).
// For example, Ramp(0, 50, time.Second, 30*time.Second) raises the rate from 0 to 50 per second over 30 seconds.
//
// The length of the whole run is set by Plan.Duration. If over is longer than Duration, to is never reached.
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
		return errors.New("jotaro: Ramp counts must be at least 0")
	case r.from == 0 && r.to == 0:
		return errors.New("jotaro: Ramp with both counts 0 never arrives")
	case r.per <= 0:
		return errors.New("jotaro: Ramp period must be positive")
	case r.over <= 0:
		return errors.New("jotaro: Ramp slope duration must be positive")
	case r.per/time.Duration(max(r.from, r.to)) <= 0:
		return errors.New("jotaro: Ramp is so high that the interval is under 1ns")
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

// Stage is one stage of Stages. It keeps Arrival going for For.
type Stage struct {
	Arrival Arrival
	For     time.Duration
}

// Stages runs several stages in order.
// The Arrival of each stage runs with the start of that stage as 0 (a Ramp's slope also starts there).
// For example, ramp up to 50/s over 30 seconds, hold for 60 seconds, then ramp down to 0 over 10 seconds:
//
//	Stages(
//		Stage{Ramp(0, 50, time.Second, 30*time.Second), 30 * time.Second},
//		Stage{Rate(50, time.Second), 60 * time.Second},
//		Stage{Ramp(50, 0, time.Second, 10*time.Second), 10 * time.Second},
//	)
//
// Arrivals stop when all stages are over. The length of the whole run is set by Plan.Duration,
// so make it match the sum of the For of every stage.
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
		return errors.New("jotaro: Stages needs at least one stage")
	}
	for i, st := range s.stages {
		switch {
		case st.Arrival == nil:
			return fmt.Errorf("jotaro: Arrival of stage %d of Stages is not set", i+1)
		case st.For <= 0:
			return fmt.Errorf("jotaro: For of stage %d of Stages must be positive", i+1)
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
