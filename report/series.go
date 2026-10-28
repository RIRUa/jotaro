package report

import (
	"slices"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// SeriesInterval is the length of one interval of the time series.
const SeriesInterval = time.Second

// SeriesPoint is the aggregate for one second since the start of the run.
type SeriesPoint struct {
	// Second is the seconds elapsed since StartedAt (0-based). The interval is [Second, Second+1) seconds.
	Second int
	// Requests is the number of requests started in this interval.
	Requests int
	// Errors is how many of them ended in a network error (Err is not empty). HTTP 4xx/5xx are not included.
	Errors int
	// HTTP5xx is how many of them returned HTTP 5xx (it does not overlap with Errors).
	HTTP5xx int
	// NewConns / ReusedConns are, among the requests started in this interval,
	// the number that opened a new connection and the number that reused one. Those that failed before getting a connection are counted in neither.
	NewConns    int
	ReusedConns int
	// Latency is the latency distribution of the requests started in this interval.
	Latency Latency
	// DNS / Connect / TLS are the distributions over the requests started in this interval in which that phase actually happened
	// (the same scope as the fields of the same name in RequestStats).
	DNS     Latency
	Connect Latency
	TLS     Latency
	// Concurrency is the maximum number of iterations running during this interval.
	Concurrency int
}

// buildSeries は記録を 1 秒ごとに集計する。
// 開始時刻の記録が無い（origin が zero）、または記録が空のときは nil を返す。
func buildSeries(origin time.Time, reqs []record.Record, iters []record.IterationRecord) []SeriesPoint {
	if origin.IsZero() {
		return nil
	}
	index := func(t time.Time) int { return max(int(t.Sub(origin)/SeriesInterval), 0) }

	// 記録がある最後の区間まで。
	n := 0
	for _, r := range reqs {
		n = max(n, index(r.Start)+1)
	}
	for _, it := range iters {
		n = max(n, index(it.Start.Add(it.Duration))+1)
	}
	if n == 0 {
		return nil
	}

	// 1 秒ごとの時間の記録。あとで分布にする。
	var (
		lats     = make([][]time.Duration, n)
		dnss     = make([][]time.Duration, n)
		connects = make([][]time.Duration, n)
		tlss     = make([][]time.Duration, n)
	)
	pts := make([]SeriesPoint, n)
	for i := range pts {
		pts[i].Second = i
	}
	for _, r := range reqs {
		i := index(r.Start)
		pts[i].Requests++
		if r.Err != record.ErrNone {
			pts[i].Errors++
		}
		if r.Code >= 500 {
			pts[i].HTTP5xx++
		}
		switch r.Conn {
		case record.ConnNew:
			pts[i].NewConns++
		case record.ConnReused:
			pts[i].ReusedConns++
		}
		lats[i] = append(lats[i], r.Latency)
		if r.DNS > 0 {
			dnss[i] = append(dnss[i], r.DNS)
		}
		if r.Connect > 0 {
			connects[i] = append(connects[i], r.Connect)
		}
		if r.TLS > 0 {
			tlss[i] = append(tlss[i], r.TLS)
		}
	}
	for i := range pts {
		pts[i].Latency = newLatencyOwned(lats[i])
		pts[i].DNS, pts[i].Connect, pts[i].TLS = newLatencyOwned(dnss[i]), newLatencyOwned(connects[i]), newLatencyOwned(tlss[i])
	}

	// 同時実行数は、開始 (+1) と終了 (-1) の順に走査して区間ごとの最大値を求める。
	type event struct {
		at    time.Duration
		delta int
	}
	events := make([]event, 0, 2*len(iters))
	for _, it := range iters {
		start := max(it.Start.Sub(origin), 0)
		events = append(events, event{start, +1}, event{start + max(it.Duration, 0), -1})
	}
	// 同時刻では終了を先に処理する（隣り合うイテレーションを重ねて数えない）。
	slices.SortFunc(events, func(a, b event) int {
		if a.at != b.at {
			return int(a.at - b.at)
		}
		return a.delta - b.delta
	})
	cur, e := 0, 0
	for i := range pts {
		end := time.Duration(i+1) * SeriesInterval
		peak := cur // 区間の頭の時点
		for ; e < len(events) && events[e].at < end; e++ {
			cur += events[e].delta
			peak = max(peak, cur)
		}
		pts[i].Concurrency = peak
	}
	return pts
}

// mergeSeries は offset 秒ずらした b を a に足し込む。
// 件数は合計し、同時実行数は同時に動いていた前提で合計する（上限の見積り）。
func mergeSeries(a, b []SeriesPoint, offset int) []SeriesPoint {
	out := slices.Clone(a)
	for _, p := range b {
		sec := p.Second + offset
		i := slices.IndexFunc(out, func(q SeriesPoint) bool { return q.Second == sec })
		if i < 0 {
			p.Second = sec
			out = append(out, p)
			continue
		}
		q := &out[i]
		q.Requests += p.Requests
		q.Errors += p.Errors
		q.HTTP5xx += p.HTTP5xx
		q.NewConns += p.NewConns
		q.ReusedConns += p.ReusedConns
		q.Latency = mergeLatency(q.Latency, p.Latency)
		q.DNS = mergeLatency(q.DNS, p.DNS)
		q.Connect = mergeLatency(q.Connect, p.Connect)
		q.TLS = mergeLatency(q.TLS, p.TLS)
		q.Concurrency += p.Concurrency
	}
	slices.SortFunc(out, func(x, y SeriesPoint) int { return x.Second - y.Second })
	return out
}
