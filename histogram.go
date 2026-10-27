package jotaro

import (
	"math"
	"slices"
	"time"
)

// 結合のために Latency が持つ対数ヒストグラム (DDSketch 方式)。
// バケット i は (gamma^(i-1), gamma^i] ns を表し、相対誤差は histAlpha 以内。
// バケットは足し算で結合できるので、複数のレポートから分布を近似的に復元できる。
const histAlpha = 0.01

var (
	histGamma    = (1 + histAlpha) / (1 - histAlpha)
	histLogGamma = math.Log(histGamma)
)

// histogram はバケット番号ごとの件数。
type histogram map[int]int

// newHistogram は ds を入れたヒストグラムを返す。ds が空なら nil。
func newHistogram(ds []time.Duration) histogram {
	if len(ds) == 0 {
		return nil
	}
	h := make(histogram)
	for _, d := range ds {
		h[histIndex(d)]++
	}
	return h
}

// histIndex は 1ns 以下をすべてバケット 0 にまとめる。
func histIndex(d time.Duration) int {
	if d <= 1 {
		return 0
	}
	return int(math.Ceil(math.Log(float64(d)) / histLogGamma))
}

// histValue はバケットの代表値。バケット内のどこにあっても相対誤差が histAlpha 以内になる。
func histValue(i int) time.Duration {
	return time.Duration(math.Round(2 * math.Pow(histGamma, float64(i)) / (histGamma + 1)))
}

func (h histogram) total() int {
	n := 0
	for _, c := range h {
		n += c
	}
	return n
}

func mergeHistograms(a, b histogram) histogram {
	out := make(histogram, max(len(a), len(b)))
	for _, h := range []histogram{a, b} {
		for i, c := range h {
			out[i] += c
		}
	}
	return out
}

// percentile は nearest-rank 法で p パーセンタイルを返す。
func (h histogram) percentile(p int) time.Duration {
	n := h.total()
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
			return histValue(i)
		}
	}
	return histValue(keys[len(keys)-1])
}
