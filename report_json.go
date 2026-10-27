package jotaro

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"time"
)

// JSONVersion は WriteJSON が出力するスキーマの版。互換を壊す変更のたびに上げる。
// ReadJSON は 1 以降のすべての版を読める。
//
//	3: series（1 秒ごとの時系列）と、requests の dns / connect / tls / conn_new / conn_reused を追加。
//	   series の conn_new / conn_reused / dns / connect / tls と、health（負荷生成側の健全性）は後から足した
//	   （無い JSON は 0・空として読む）
//	2: started_at と、latency の buckets (ヒストグラム) を追加
//	1: 初版
const JSONVersion = 3

// 出力用の型。Report の内部表現を変えても JSON の形が変わらないよう、別に持つ。
// 時間はすべてナノ秒の整数（フィールド名は _ns で終える）。

type jsonLatency struct {
	Count  int   `json:"count"`
	MinNS  int64 `json:"min_ns"`
	MeanNS int64 `json:"mean_ns"`
	P50NS  int64 `json:"p50_ns"`
	P90NS  int64 `json:"p90_ns"`
	P95NS  int64 `json:"p95_ns"`
	P99NS  int64 `json:"p99_ns"`
	MaxNS  int64 `json:"max_ns"`
	// Buckets は対数ヒストグラム（バケット番号 → 件数）。結合時のパーセンタイルの再計算に使う。
	Buckets map[int]int `json:"buckets"`
}

type jsonRequests struct {
	Count     int         `json:"count"`
	Latency   jsonLatency `json:"latency"`
	TTFB      jsonLatency `json:"ttfb"`
	Corrected jsonLatency `json:"corrected"`
	DNS       jsonLatency `json:"dns"`
	Connect   jsonLatency `json:"connect"`
	TLS       jsonLatency `json:"tls"`
	// ConnNew / ConnReused は接続を新しく張った / 再利用したリクエストの数。
	ConnNew    int            `json:"conn_new"`
	ConnReused int            `json:"conn_reused"`
	Status     map[string]int `json:"status"`
	Errors     map[string]int `json:"errors"`
	BytesIn    int64          `json:"bytes_in"`
	BytesOut   int64          `json:"bytes_out"`
}

type jsonStep struct {
	Name     string         `json:"name"`
	Count    int            `json:"count"`
	Failed   int            `json:"failed"`
	Duration jsonLatency    `json:"duration"`
	Errors   map[string]int `json:"errors"`
	Requests jsonRequests   `json:"requests"`
}

type jsonIterations struct {
	Count  int            `json:"count"`
	Failed int            `json:"failed"`
	Active jsonLatency    `json:"active"` // Think を除いた時間
	Total  jsonLatency    `json:"total"`  // Think を含む時間
	Errors map[string]int `json:"errors"`
}

type jsonScenario struct {
	Name       string         `json:"name"`
	Iterations jsonIterations `json:"iterations"`
	Steps      []jsonStep     `json:"steps"`
}

type jsonSeriesPoint struct {
	Second      int         `json:"second"`
	Requests    int         `json:"requests"`
	Errors      int         `json:"errors"`
	HTTP5xx     int         `json:"http_5xx"`
	ConnNew     int         `json:"conn_new"`
	ConnReused  int         `json:"conn_reused"`
	Latency     jsonLatency `json:"latency"`
	DNS         jsonLatency `json:"dns"`
	Connect     jsonLatency `json:"connect"`
	TLS         jsonLatency `json:"tls"`
	Concurrency int         `json:"concurrency"`
}

type jsonHealthPoint struct {
	Second     int     `json:"second"`
	Goroutines int     `json:"goroutines"`
	CPU        float64 `json:"cpu"` // 0〜1。取得できなかったときは -1
}

type jsonReport struct {
	Version   int               `json:"version"`
	Open      bool              `json:"open"`
	Dropped   int               `json:"dropped"`
	StartedAt time.Time         `json:"started_at"`
	ElapsedNS int64             `json:"elapsed_ns"`
	RPS       float64           `json:"rps"`
	Requests  jsonRequests      `json:"requests"`
	Scenarios []jsonScenario    `json:"scenarios"`
	Series    []jsonSeriesPoint `json:"series"`
	Health    []jsonHealthPoint `json:"health"`
}

// WriteJSON はレポートを JSON として w に書き出す。時間はナノ秒の整数。
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r.toJSON())
}

func (r *Report) toJSON() jsonReport {
	out := jsonReport{
		Version:   JSONVersion,
		Open:      r.Open,
		Dropped:   r.Dropped,
		StartedAt: r.StartedAt,
		ElapsedNS: ns(r.Elapsed),
		RPS:       r.RPS(),
		Requests:  toJSONRequests(r.Requests),
		Scenarios: []jsonScenario{}, // null ではなく [] にする
		Series:    []jsonSeriesPoint{},
		Health:    []jsonHealthPoint{},
	}
	for _, sc := range r.Scenarios {
		js := jsonScenario{
			Name: sc.Name,
			Iterations: jsonIterations{
				Count:  sc.Iterations.Count,
				Failed: sc.Iterations.Failed,
				Active: toJSONLatency(sc.Iterations.Active),
				Total:  toJSONLatency(sc.Iterations.Total),
				Errors: errCounts(sc.Iterations.Errors),
			},
			Steps: []jsonStep{},
		}
		for _, s := range sc.Steps {
			js.Steps = append(js.Steps, jsonStep{
				Name:     s.Name,
				Count:    s.Count,
				Failed:   s.Failed,
				Duration: toJSONLatency(s.Duration),
				Errors:   errCounts(s.Errors),
				Requests: toJSONRequests(s.Requests),
			})
		}
		out.Scenarios = append(out.Scenarios, js)
	}
	for _, p := range r.Series {
		out.Series = append(out.Series, jsonSeriesPoint{
			Second:      p.Second,
			Requests:    p.Requests,
			Errors:      p.Errors,
			HTTP5xx:     p.HTTP5xx,
			ConnNew:     p.NewConns,
			ConnReused:  p.ReusedConns,
			Latency:     toJSONLatency(p.Latency),
			DNS:         toJSONLatency(p.DNS),
			Connect:     toJSONLatency(p.Connect),
			TLS:         toJSONLatency(p.TLS),
			Concurrency: p.Concurrency,
		})
	}
	for _, p := range r.Health {
		out.Health = append(out.Health, jsonHealthPoint{Second: p.Second, Goroutines: p.Goroutines, CPU: p.CPU})
	}
	return out
}

func toJSONRequests(q RequestStats) jsonRequests {
	status := make(map[string]int, len(q.Status))
	for k, v := range q.Status {
		status[k] = v
	}
	return jsonRequests{
		Count:      q.Count,
		Latency:    toJSONLatency(q.Latency),
		TTFB:       toJSONLatency(q.TTFB),
		Corrected:  toJSONLatency(q.Corrected),
		DNS:        toJSONLatency(q.DNS),
		Connect:    toJSONLatency(q.Connect),
		TLS:        toJSONLatency(q.TLS),
		ConnNew:    q.NewConns,
		ConnReused: q.ReusedConns,
		Status:     status,
		Errors:     errCounts(q.Errors),
		BytesIn:    q.BytesIn,
		BytesOut:   q.BytesOut,
	}
}

func toJSONLatency(l Latency) jsonLatency {
	return jsonLatency{
		Count:  l.Count,
		MinNS:  ns(l.Min),
		MeanNS: ns(l.Mean),
		P50NS:  ns(l.P50),
		P90NS:  ns(l.P90),
		P95NS:  ns(l.P95),
		P99NS:  ns(l.P99),
		MaxNS:  ns(l.Max),
		// null ではなく {} にする
		Buckets: maps.Clone(bucketsOrEmpty(l.hist)),
	}
}

// errCounts は nil でも {} になるよう、空でも必ず map を返す。
func errCounts(m map[ErrKind]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

func ns(d time.Duration) int64 { return d.Nanoseconds() }

// ReadJSON は WriteJSON が書いた JSON を Report に戻す。
// version が対応していないときは error を返す。
func ReadJSON(r io.Reader) (*Report, error) {
	var in jsonReport
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("jotaro: read report: %w", err)
	}
	if in.Version < 1 || in.Version > JSONVersion {
		return nil, fmt.Errorf("jotaro: unsupported report version %d (want 1..%d)", in.Version, JSONVersion)
	}
	return in.toReport(), nil
}

// toReport は toJSON の逆。空の map / スライスは nil に戻す（BuildReport の結果と同じ形）。
func (j jsonReport) toReport() *Report {
	rep := &Report{
		Open:      j.Open,
		Dropped:   j.Dropped,
		StartedAt: wallUTC(j.StartedAt),
		Elapsed:   time.Duration(j.ElapsedNS),
		Requests:  fromJSONRequests(j.Requests),
	}
	for _, js := range j.Scenarios {
		sc := ScenarioReport{
			Name: js.Name,
			Iterations: IterationStats{
				Count:  js.Iterations.Count,
				Failed: js.Iterations.Failed,
				Active: fromJSONLatency(js.Iterations.Active),
				Total:  fromJSONLatency(js.Iterations.Total),
				Errors: fromErrCounts(js.Iterations.Errors),
			},
		}
		for _, s := range js.Steps {
			sc.Steps = append(sc.Steps, StepStats{
				Name:     s.Name,
				Count:    s.Count,
				Failed:   s.Failed,
				Duration: fromJSONLatency(s.Duration),
				Errors:   fromErrCounts(s.Errors),
				Requests: fromJSONRequests(s.Requests),
			})
		}
		rep.Scenarios = append(rep.Scenarios, sc)
	}
	for _, p := range j.Series {
		rep.Series = append(rep.Series, SeriesPoint{
			Second:      p.Second,
			Requests:    p.Requests,
			Errors:      p.Errors,
			HTTP5xx:     p.HTTP5xx,
			NewConns:    p.ConnNew,
			ReusedConns: p.ConnReused,
			Latency:     fromJSONLatency(p.Latency),
			DNS:         fromJSONLatency(p.DNS),
			Connect:     fromJSONLatency(p.Connect),
			TLS:         fromJSONLatency(p.TLS),
			Concurrency: p.Concurrency,
		})
	}
	for _, p := range j.Health {
		rep.Health = append(rep.Health, HealthPoint{Second: p.Second, Goroutines: p.Goroutines, CPU: p.CPU})
	}
	return rep
}

func fromJSONRequests(q jsonRequests) RequestStats {
	var status map[string]int
	if len(q.Status) > 0 {
		status = q.Status
	}
	return RequestStats{
		Count:       q.Count,
		Latency:     fromJSONLatency(q.Latency),
		TTFB:        fromJSONLatency(q.TTFB),
		Corrected:   fromJSONLatency(q.Corrected),
		DNS:         fromJSONLatency(q.DNS),
		Connect:     fromJSONLatency(q.Connect),
		TLS:         fromJSONLatency(q.TLS),
		NewConns:    q.ConnNew,
		ReusedConns: q.ConnReused,
		Status:      status,
		Errors:      fromErrCounts(q.Errors),
		BytesIn:     q.BytesIn,
		BytesOut:    q.BytesOut,
	}
}

func fromJSONLatency(l jsonLatency) Latency {
	return Latency{
		Count: l.Count,
		Min:   time.Duration(l.MinNS),
		Mean:  time.Duration(l.MeanNS),
		P50:   time.Duration(l.P50NS),
		P90:   time.Duration(l.P90NS),
		P95:   time.Duration(l.P95NS),
		P99:   time.Duration(l.P99NS),
		Max:   time.Duration(l.MaxNS),
		hist:  nilIfEmpty(l.Buckets),
	}
}

func fromErrCounts(m map[string]int) map[ErrKind]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[ErrKind]int, len(m))
	for k, v := range m {
		out[ErrKind(k)] = v
	}
	return out
}

// bucketsOrEmpty は nil でも空の map を返す。
func bucketsOrEmpty(h histogram) map[int]int {
	if h == nil {
		return map[int]int{}
	}
	return h
}

func nilIfEmpty(m map[int]int) histogram {
	if len(m) == 0 {
		return nil
	}
	return m
}
