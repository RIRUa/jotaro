package report

import (
	"fmt"
	"slices"
	"time"

	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
)

// Latency は時間の分布。
type Latency struct {
	Count int
	Min   time.Duration
	Mean  time.Duration
	P50   time.Duration
	P90   time.Duration
	P95   time.Duration
	P99   time.Duration
	Max   time.Duration

	// hist は分布のヒストグラム。MergeReports がパーセンタイルを求め直すために使う。
	hist histogram.H
}

func newLatency(ds []time.Duration) Latency {
	if len(ds) == 0 {
		return Latency{}
	}
	s := slices.Clone(ds)
	slices.Sort(s)

	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	return Latency{
		Count: len(s),
		Min:   s[0],
		Mean:  sum / time.Duration(len(s)),
		P50:   percentile(s, 50),
		P90:   percentile(s, 90),
		P95:   percentile(s, 95),
		P99:   percentile(s, 99),
		Max:   s[len(s)-1],
		hist:  histogram.New(s),
	}
}

// percentile は昇順にソート済みの s から nearest-rank 法で p パーセンタイルを返す。
func percentile(s []time.Duration, p int) time.Duration {
	rank := (p*len(s) + 99) / 100 // ceil(p/100 * n)
	return s[max(rank, 1)-1]
}

// RequestStats はリクエスト単位の集計。
type RequestStats struct {
	Count   int
	Latency Latency // 通信エラーのリクエストも含む
	TTFB    Latency // 応答を受け取れたリクエストのみ
	// DNS / Connect / TLS は、その区間が実際に起きたリクエストのみ
	// （接続を再利用したリクエストや、http の TLS は含まれない）。
	DNS     Latency
	Connect Latency
	TLS     Latency
	// NewConns / ReusedConns は、接続を新しく張ったリクエストと再利用したリクエストの数。
	NewConns    int
	ReusedConns int
	// Corrected は予定開始時刻からの時間（Coordinated Omission の補正後）。
	// Users モデルでは Latency と同じ。
	Corrected Latency

	Status map[string]int         // "2xx" のようなステータスクラスごとの件数
	Errors map[record.ErrKind]int // 通信エラーの内訳

	BytesIn  int64
	BytesOut int64
}

// ReuseRate は接続を再利用したリクエストの割合（0〜1）。接続の記録が無ければ 0。
func (q RequestStats) ReuseRate() float64 {
	if n := q.NewConns + q.ReusedConns; n > 0 {
		return float64(q.ReusedConns) / float64(n)
	}
	return 0
}

// StepStats は u.Step 単位の集計。
type StepStats struct {
	Name     string
	Count    int
	Failed   int
	Duration Latency
	Errors   map[record.ErrKind]int // ステップの失敗の内訳
	Requests RequestStats
}

// IterationStats はシナリオ 1 回分の集計。
type IterationStats struct {
	Count  int
	Failed int
	Active Latency // Think を除いた時間
	Total  Latency // Think を含む時間
	Errors map[record.ErrKind]int
}

// ScenarioReport はシナリオごとの集計。
type ScenarioReport struct {
	Name       string
	Iterations IterationStats
	Steps      []StepStats // 最初に実行された順
}

// Report は実行全体の集計結果。
type Report struct {
	// Open は Arrival（オープンモデル）で実行したか。
	Open bool
	// Dropped は、同時実行数の上限のために実行しなかった到着の数。
	Dropped int

	// StartedAt は実行を始めた時刻 (UTC)。不明なら zero。
	StartedAt time.Time
	Elapsed   time.Duration
	Requests  RequestStats
	Scenarios []ScenarioReport // 最初に実行された順
	// Series は StartedAt から 1 秒ごとの集計。StartedAt が無いときは nil。
	Series []SeriesPoint
	// Health は StartedAt から 1 秒ごとの、負荷生成側の健全性。Run で実行したときだけ入る。
	Health []record.HealthPoint
}

// RPS は 1 秒あたりのリクエスト数。
func (r *Report) RPS() float64 {
	return perSecond(r.Requests.Count, r.Elapsed)
}

func perSecond(n int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(n) / elapsed.Seconds()
}

// FromRaw は生データを集計する。Run が返す Report と同じ集計になる。
func FromRaw(raw *record.Raw) *Report {
	rep := BuildReport(raw.Requests, raw.Steps, raw.Iterations, raw.Elapsed)
	rep.Open = raw.Open
	rep.Dropped = raw.Dropped
	if !raw.StartedAt.IsZero() {
		rep.StartedAt = wallUTC(raw.StartedAt)
	}
	rep.Health = raw.Health
	return rep
}

// BuildReport は記録から集計する。
// elapsed が 0 のときは、記録の最初の開始から最後の終了までを実行時間とする。
func BuildReport(reqs []record.Record, steps []record.StepRecord, iters []record.IterationRecord, elapsed time.Duration) *Report {
	if elapsed == 0 {
		elapsed = observedSpan(reqs, iters)
	}
	rep := &Report{StartedAt: observedStart(reqs, steps, iters), Elapsed: elapsed, Requests: requestStats(reqs)}
	rep.Series = buildSeries(rep.StartedAt, reqs, iters)

	type stepKey struct{ scenario, step string }
	var (
		scenarios []*ScenarioReport
		byName    = map[string]*ScenarioReport{}
		stepDurs  = map[stepKey][]time.Duration{}
		stepAt    = map[stepKey]int{} // sc.Steps 内の位置。append で動くのでポインタでは持たない
		stepReqs  = map[stepKey][]record.Record{}
	)
	scenario := func(name string) *ScenarioReport {
		sc, ok := byName[name]
		if !ok {
			sc = &ScenarioReport{Name: name}
			byName[name] = sc
			scenarios = append(scenarios, sc)
		}
		return sc
	}

	for _, r := range reqs {
		k := stepKey{r.Scenario, r.Step}
		stepReqs[k] = append(stepReqs[k], r)
	}

	// 開始時刻順に並べて、「最初に実行された順」を決める。
	sortedIters := slices.Clone(iters)
	slices.SortStableFunc(sortedIters, func(a, b record.IterationRecord) int { return a.Start.Compare(b.Start) })
	sortedSteps := slices.Clone(steps)
	slices.SortStableFunc(sortedSteps, func(a, b record.StepRecord) int { return a.Start.Compare(b.Start) })

	var (
		activeDurs = map[string][]time.Duration{}
		totalDurs  = map[string][]time.Duration{}
	)
	for _, it := range sortedIters {
		sc := scenario(it.Scenario)
		sc.Iterations.Count++
		if it.Err != record.ErrNone {
			sc.Iterations.Failed++
			sc.Iterations.Errors = count(sc.Iterations.Errors, it.Err)
		}
		activeDurs[it.Scenario] = append(activeDurs[it.Scenario], it.Active())
		totalDurs[it.Scenario] = append(totalDurs[it.Scenario], it.Duration)
	}
	for _, st := range sortedSteps {
		sc := scenario(st.Scenario)
		k := stepKey{st.Scenario, st.Step}
		i, ok := stepAt[k]
		if !ok {
			sc.Steps = append(sc.Steps, StepStats{Name: st.Step})
			i = len(sc.Steps) - 1
			stepAt[k] = i
		}
		ss := &sc.Steps[i]
		ss.Count++
		if st.Err != record.ErrNone {
			ss.Failed++
			ss.Errors = count(ss.Errors, st.Err)
		}
		stepDurs[k] = append(stepDurs[k], st.Duration)
	}
	// ステップの記録が無くリクエストだけあるシナリオも拾う。
	for k := range stepReqs {
		scenario(k.scenario)
	}

	for _, sc := range scenarios {
		sc.Iterations.Active = newLatency(activeDurs[sc.Name])
		sc.Iterations.Total = newLatency(totalDurs[sc.Name])
		for i := range sc.Steps {
			ss := &sc.Steps[i]
			k := stepKey{sc.Name, ss.Name}
			ss.Duration = newLatency(stepDurs[k])
			ss.Requests = requestStats(stepReqs[k])
		}
		rep.Scenarios = append(rep.Scenarios, *sc)
	}
	return rep
}

func requestStats(reqs []record.Record) RequestStats {
	st := RequestStats{Count: len(reqs)}
	var lat, ttfb, corrected, dns, connect, tlsd []time.Duration
	for _, r := range reqs {
		lat = append(lat, r.Latency)
		corrected = append(corrected, r.Corrected())
		if r.TTFB > 0 {
			ttfb = append(ttfb, r.TTFB)
		}
		for _, p := range []struct {
			d   time.Duration
			dst *[]time.Duration
		}{{r.DNS, &dns}, {r.Connect, &connect}, {r.TLS, &tlsd}} {
			if p.d > 0 {
				*p.dst = append(*p.dst, p.d)
			}
		}
		switch r.Conn {
		case record.ConnNew:
			st.NewConns++
		case record.ConnReused:
			st.ReusedConns++
		}
		if r.Code > 0 {
			if st.Status == nil {
				st.Status = map[string]int{}
			}
			st.Status[fmt.Sprintf("%dxx", r.Code/100)]++
		}
		if r.Err != record.ErrNone {
			st.Errors = count(st.Errors, r.Err)
		}
		st.BytesIn += r.BytesIn
		st.BytesOut += r.BytesOut
	}
	st.Latency = newLatency(lat)
	st.TTFB = newLatency(ttfb)
	st.Corrected = newLatency(corrected)
	st.DNS, st.Connect, st.TLS = newLatency(dns), newLatency(connect), newLatency(tlsd)
	return st
}

func count(m map[record.ErrKind]int, k record.ErrKind) map[record.ErrKind]int {
	if m == nil {
		m = map[record.ErrKind]int{}
	}
	m[k]++
	return m
}

// observedSpan は記録から実行時間を推定する。
func observedSpan(reqs []record.Record, iters []record.IterationRecord) time.Duration {
	var first, last time.Time
	extend := func(start time.Time, d time.Duration) {
		if first.IsZero() || start.Before(first) {
			first = start
		}
		if end := start.Add(d); end.After(last) {
			last = end
		}
	}
	for _, r := range reqs {
		extend(r.Start, r.Latency)
	}
	for _, it := range iters {
		extend(it.Start, it.Duration)
	}
	if first.IsZero() {
		return 0
	}
	return last.Sub(first)
}

// observedStart は記録の中で一番早い開始時刻を返す。記録が無ければ zero。
func observedStart(reqs []record.Record, steps []record.StepRecord, iters []record.IterationRecord) time.Time {
	var first time.Time
	extend := func(t time.Time) {
		if !t.IsZero() && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	for _, r := range reqs {
		extend(r.Start)
	}
	for _, st := range steps {
		extend(st.Start)
	}
	for _, it := range iters {
		extend(it.Start)
	}
	return wallUTC(first)
}

// wallUTC は単調時計とタイムゾーンを落とす。JSON を往復しても同じ値になる。
func wallUTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.Round(0).UTC()
}
