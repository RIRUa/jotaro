package jotaro

import (
	"slices"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// SeriesInterval は時系列の 1 区間の長さ。
const SeriesInterval = time.Second

// SeriesPoint は実行開始から 1 秒ごとの集計。
type SeriesPoint struct {
	// Second は StartedAt からの経過秒（0 始まり）。区間は [Second, Second+1) 秒。
	Second int
	// Requests はこの区間に開始したリクエスト数。
	Requests int
	// Errors はそのうち通信エラー（Err が空でない）になった数。HTTP 4xx/5xx は含まない。
	Errors int
	// HTTP5xx はそのうち HTTP 5xx を返した数（Errors とは重ならない）。
	HTTP5xx int
	// NewConns / ReusedConns は、この区間に開始したリクエストのうち、
	// 接続を新しく張ったものと再利用したものの数。接続を得る前に失敗したものはどちらにも数えない。
	NewConns    int
	ReusedConns int
	// Latency はこの区間に開始したリクエストのレイテンシ分布。
	Latency Latency
	// DNS / Connect / TLS は、この区間に開始したリクエストのうち、その区間が実際に起きたものの分布
	// （RequestStats の同名のフィールドと同じ範囲）。
	DNS     Latency
	Connect Latency
	TLS     Latency
	// Concurrency はこの区間に実行中だったイテレーション数の最大値。
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
		pts[i].Latency = newLatency(lats[i])
		pts[i].DNS, pts[i].Connect, pts[i].TLS = newLatency(dnss[i]), newLatency(connects[i]), newLatency(tlss[i])
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
