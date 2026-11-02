// Package histogram is the log histogram (DDSketch style) that Latency holds for merging.
//
// Bucket i covers (gamma^(i-1), gamma^i] ns, with a relative error within Alpha.
// Buckets can be merged by addition, so the distribution can be approximately restored from several reports.
package histogram

import (
	"math"
	"slices"
	"time"
)

// Alpha is the upper bound of the relative error of a representative value.
const Alpha = 0.01

var (
	// gamma is the growth factor between neighbouring bucket boundaries.
	// Choosing (1+Alpha)/(1-Alpha) is what bounds the relative error by Alpha.
	gamma = (1 + Alpha) / (1 - Alpha)
	// logGamma is cached because index runs once per observation.
	logGamma = math.Log(gamma)
)

// H is the count per bucket number.
type H map[int]int

// New returns a histogram holding ds, or nil if ds is empty.
func New(ds []time.Duration) H {
	if len(ds) == 0 {
		return nil
	}
	var h H
	for _, d := range ds {
		h.Add(d)
	}
	return h
}

// Add counts one observation, creating the histogram on first use.
func (h *H) Add(d time.Duration) {
	if *h == nil {
		*h = make(H)
	}
	(*h)[index(d)]++
}

// index puts everything at or below 1ns into bucket 0.
func index(d time.Duration) int {
	if d <= 1 {
		return 0
	}
	return int(math.Ceil(math.Log(float64(d)) / logGamma))
}

// value is the representative value of a bucket. Wherever a sample lies in the bucket, the relative error is within Alpha.
func value(i int) time.Duration {
	return time.Duration(math.Round(2 * math.Pow(gamma, float64(i)) / (gamma + 1)))
}

// Total is the sum of the counts of all buckets.
func (h H) Total() int {
	n := 0
	for _, c := range h {
		n += c
	}
	return n
}

// Merge adds two histograms together.
func Merge(a, b H) H {
	out := make(H, max(len(a), len(b)))
	for _, h := range []H{a, b} {
		for i, c := range h {
			out[i] += c
		}
	}
	return out
}

// Percentile returns the p-th percentile by the nearest-rank method. h must not be empty.
func (h H) Percentile(p int) time.Duration {
	n := h.Total()
	rank := max((p*n+99)/100, 1)
	keys := make([]int, 0, len(h))
	for i := range h {
		keys = append(keys, i)
	}
	slices.Sort(keys)
	seen := 0
	for _, i := range keys {
		seen += h[i]
		if seen >= rank {
			return value(i)
		}
	}
	return value(keys[len(keys)-1])
}
