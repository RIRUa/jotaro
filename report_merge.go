package jotaro

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// MergeReports は複数のレポートを 1 つにまとめる。
// 負荷生成機を分けて同時に実行した結果を、1 回の実行として見たいときに使う。
//
//   - 件数・バイト数・ステータス・エラーは合計する。
//   - 実行時間は、最初の StartedAt から最後の終了 (StartedAt + Elapsed) まで。
//     StartedAt が無いレポート (version 1 の JSON など) が混じるときは、
//     Elapsed の最大値（同時に実行した前提）にする。RPS は合計件数 / Elapsed になる。
//   - 時系列 (Series) は、StartedAt の差だけ秒をずらして重ね、区間ごとに足す。
//     同時実行数は同時に動いていた前提の合計（上限の見積り）。
//   - シナリオとステップは名前で突き合わせ、並びは最初に現れた順。
//   - Min / Max / Mean は正確。P50〜P99 はヒストグラムを足して求め直すので、
//     相対誤差 1% 以内の近似になる。ヒストグラムが無い (version 1 の JSON など)
//     ものが混じるときは、件数で重み付けした平均（誤差は大きい）。
//
// Open が揃っていないレポートは混ぜられないので error を返す。
func MergeReports(reps ...*Report) (*Report, error) {
	if len(reps) == 0 {
		return nil, errors.New("jotaro: merge: no reports")
	}
	out := &Report{Open: reps[0].Open}
	var reportSeries []*Report
	var (
		first, last time.Time
		allStarted  = true // すべてのレポートが StartedAt を持つか
	)
	var (
		scenarios []*ScenarioReport
		byName    = map[string]*ScenarioReport{}
		stepAt    = map[string]map[string]int{} // シナリオ名 → ステップ名 → Steps 内の位置
	)
	for i, r := range reps {
		if r.Open != out.Open {
			return nil, fmt.Errorf("jotaro: merge: report #%d has Open=%t, but #1 has Open=%t", i+1, r.Open, out.Open)
		}
		out.Dropped += r.Dropped
		out.Elapsed = max(out.Elapsed, r.Elapsed)
		if r.StartedAt.IsZero() {
			allStarted = false
		} else {
			if first.IsZero() || r.StartedAt.Before(first) {
				first = r.StartedAt
			}
			if end := r.StartedAt.Add(r.Elapsed); end.After(last) {
				last = end
			}
		}
		out.Requests = mergeRequests(out.Requests, r.Requests)
		reportSeries = append(reportSeries, r)

		for _, sc := range r.Scenarios {
			dst, ok := byName[sc.Name]
			if !ok {
				dst = &ScenarioReport{Name: sc.Name}
				byName[sc.Name] = dst
				stepAt[sc.Name] = map[string]int{}
				scenarios = append(scenarios, dst)
			}
			dst.Iterations = mergeIterations(dst.Iterations, sc.Iterations)
			for _, st := range sc.Steps {
				j, ok := stepAt[sc.Name][st.Name]
				if !ok {
					dst.Steps = append(dst.Steps, StepStats{Name: st.Name})
					j = len(dst.Steps) - 1
					stepAt[sc.Name][st.Name] = j
				}
				dst.Steps[j] = mergeStep(dst.Steps[j], st)
			}
		}
	}
	if allStarted {
		out.StartedAt = first
		out.Elapsed = last.Sub(first)
	}
	// 時系列は、StartedAt の差だけ秒をずらして重ねる。StartedAt が無いレポートがあるときは先頭を揃える。
	for _, r := range reportSeries {
		offset := 0
		if allStarted {
			offset = int((r.StartedAt.Sub(first) + SeriesInterval/2) / SeriesInterval)
		}
		out.Series = mergeSeries(out.Series, r.Series, offset)
	}
	for _, sc := range scenarios {
		out.Scenarios = append(out.Scenarios, *sc)
	}
	return out, nil
}

func mergeStep(a, b StepStats) StepStats {
	return StepStats{
		Name:     a.Name,
		Count:    a.Count + b.Count,
		Failed:   a.Failed + b.Failed,
		Duration: mergeLatency(a.Duration, b.Duration),
		Errors:   mergeErrs(a.Errors, b.Errors),
		Requests: mergeRequests(a.Requests, b.Requests),
	}
}

func mergeIterations(a, b IterationStats) IterationStats {
	return IterationStats{
		Count:  a.Count + b.Count,
		Failed: a.Failed + b.Failed,
		Active: mergeLatency(a.Active, b.Active),
		Total:  mergeLatency(a.Total, b.Total),
		Errors: mergeErrs(a.Errors, b.Errors),
	}
}

func mergeRequests(a, b RequestStats) RequestStats {
	out := RequestStats{
		Count:       a.Count + b.Count,
		Latency:     mergeLatency(a.Latency, b.Latency),
		TTFB:        mergeLatency(a.TTFB, b.TTFB),
		Corrected:   mergeLatency(a.Corrected, b.Corrected),
		DNS:         mergeLatency(a.DNS, b.DNS),
		Connect:     mergeLatency(a.Connect, b.Connect),
		TLS:         mergeLatency(a.TLS, b.TLS),
		NewConns:    a.NewConns + b.NewConns,
		ReusedConns: a.ReusedConns + b.ReusedConns,
		Errors:      mergeErrs(a.Errors, b.Errors),
		BytesIn:     a.BytesIn + b.BytesIn,
		BytesOut:    a.BytesOut + b.BytesOut,
	}
	for _, m := range []map[string]int{a.Status, b.Status} {
		for k, v := range m {
			if out.Status == nil {
				out.Status = map[string]int{}
			}
			out.Status[k] += v
		}
	}
	return out
}

func mergeErrs(a, b map[ErrKind]int) map[ErrKind]int {
	var out map[ErrKind]int
	for _, m := range []map[ErrKind]int{a, b} {
		for k, v := range m {
			if out == nil {
				out = map[ErrKind]int{}
			}
			out[k] += v
		}
	}
	return out
}

// mergeLatency は 2 つの分布をまとめる。件数 0 の側は無視する。
func mergeLatency(a, b Latency) Latency {
	switch {
	case a.Count == 0:
		return b
	case b.Count == 0:
		return a
	}
	wa, wb := float64(a.Count), float64(b.Count)
	avg := func(x, y time.Duration) time.Duration {
		return time.Duration(math.Round((float64(x)*wa + float64(y)*wb) / (wa + wb)))
	}
	out := Latency{
		Count: a.Count + b.Count,
		Min:   min(a.Min, b.Min),
		Mean:  avg(a.Mean, b.Mean),
		Max:   max(a.Max, b.Max),
	}
	// ヒストグラムが両方にあり、件数と合っているときだけ、分布からパーセンタイルを求める。
	if a.hist.total() == a.Count && b.hist.total() == b.Count {
		out.hist = mergeHistograms(a.hist, b.hist)
		// 代表値は範囲の外に出うるので、実測の Min / Max に収める。
		pct := func(p int) time.Duration { return min(max(out.hist.percentile(p), out.Min), out.Max) }
		out.P50, out.P90, out.P95, out.P99 = pct(50), pct(90), pct(95), pct(99)
		return out
	}
	out.P50, out.P90, out.P95, out.P99 = avg(a.P50, b.P50), avg(a.P90, b.P90), avg(a.P95, b.P95), avg(a.P99, b.P99)
	return out
}
