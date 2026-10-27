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
//   - Elapsed は最大値（同時に実行した前提）。RPS は合計件数 / Elapsed になる。
//   - シナリオとステップは名前で突き合わせ、並びは最初に現れた順。
//   - Min / Max は正確。Mean と P50〜P99 は件数で重み付けした平均（近似）。
//     集計済みの値からは元の分布を復元できないので、パーセンタイルは厳密にならない。
//
// Open が揃っていないレポートは混ぜられないので error を返す。
func MergeReports(reps ...*Report) (*Report, error) {
	if len(reps) == 0 {
		return nil, errors.New("jotaro: merge: no reports")
	}
	out := &Report{Open: reps[0].Open}
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
		out.Requests = mergeRequests(out.Requests, r.Requests)

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
		Count:     a.Count + b.Count,
		Latency:   mergeLatency(a.Latency, b.Latency),
		TTFB:      mergeLatency(a.TTFB, b.TTFB),
		Corrected: mergeLatency(a.Corrected, b.Corrected),
		Errors:    mergeErrs(a.Errors, b.Errors),
		BytesIn:   a.BytesIn + b.BytesIn,
		BytesOut:  a.BytesOut + b.BytesOut,
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
	return Latency{
		Count: a.Count + b.Count,
		Min:   min(a.Min, b.Min),
		Mean:  avg(a.Mean, b.Mean),
		P50:   avg(a.P50, b.P50),
		P90:   avg(a.P90, b.P90),
		P95:   avg(a.P95, b.P95),
		P99:   avg(a.P99, b.P99),
		Max:   max(a.Max, b.Max),
	}
}
