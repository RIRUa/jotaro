// Package histogram は、結合のために Latency が持つ対数ヒストグラム (DDSketch 方式)。
//
// バケット i は (gamma^(i-1), gamma^i] ns を表し、相対誤差は Alpha 以内。
// バケットは足し算で結合できるので、複数のレポートから分布を近似的に復元できる。
package histogram

import (
	"math"
	"slices"
	"time"
)

// Alpha は代表値の相対誤差の上限。
const Alpha = 0.01

var (
	// gamma is the growth factor between neighbouring bucket boundaries.
	// Choosing (1+Alpha)/(1-Alpha) is what bounds the relative error by Alpha.
	gamma = (1 + Alpha) / (1 - Alpha)
	// logGamma is cached because index runs once per observation.
	logGamma = math.Log(gamma)
)

// H はバケット番号ごとの件数。
type H map[int]int

// New は ds を入れたヒストグラムを返す。ds が空なら nil。
func New(ds []time.Duration) H {
	if len(ds) == 0 {
		return nil
	}
	h := make(H)
	for _, d := range ds {
		h[index(d)]++
	}
	return h
}

// index は 1ns 以下をすべてバケット 0 にまとめる。
func index(d time.Duration) int {
	if d <= 1 {
		return 0
	}
	return int(math.Ceil(math.Log(float64(d)) / logGamma))
}

// value はバケットの代表値。バケット内のどこにあっても相対誤差が Alpha 以内になる。
func value(i int) time.Duration {
	return time.Duration(math.Round(2 * math.Pow(gamma, float64(i)) / (gamma + 1)))
}

// Total は全バケットの件数の合計。
func (h H) Total() int {
	n := 0
	for _, c := range h {
		n += c
	}
	return n
}

// Merge は 2 つのヒストグラムを足し合わせる。
func Merge(a, b H) H {
	out := make(H, max(len(a), len(b)))
	for _, h := range []H{a, b} {
		for i, c := range h {
			out[i] += c
		}
	}
	return out
}

// Percentile は nearest-rank 法で p パーセンタイルを返す。h は空でないこと。
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
