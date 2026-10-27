package jotaro

import (
	"encoding/json"
	"io"
	"time"
)

// JSONVersion は WriteJSON が出力するスキーマの版。互換を壊す変更のたびに上げる。
const JSONVersion = 1

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
}

type jsonRequests struct {
	Count     int            `json:"count"`
	Latency   jsonLatency    `json:"latency"`
	TTFB      jsonLatency    `json:"ttfb"`
	Corrected jsonLatency    `json:"corrected"`
	Status    map[string]int `json:"status"`
	Errors    map[string]int `json:"errors"`
	BytesIn   int64          `json:"bytes_in"`
	BytesOut  int64          `json:"bytes_out"`
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

type jsonReport struct {
	Version   int            `json:"version"`
	Open      bool           `json:"open"`
	Dropped   int            `json:"dropped"`
	ElapsedNS int64          `json:"elapsed_ns"`
	RPS       float64        `json:"rps"`
	Requests  jsonRequests   `json:"requests"`
	Scenarios []jsonScenario `json:"scenarios"`
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
		ElapsedNS: ns(r.Elapsed),
		RPS:       r.RPS(),
		Requests:  toJSONRequests(r.Requests),
		Scenarios: []jsonScenario{}, // null ではなく [] にする
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
	return out
}

func toJSONRequests(q RequestStats) jsonRequests {
	status := make(map[string]int, len(q.Status))
	for k, v := range q.Status {
		status[k] = v
	}
	return jsonRequests{
		Count:     q.Count,
		Latency:   toJSONLatency(q.Latency),
		TTFB:      toJSONLatency(q.TTFB),
		Corrected: toJSONLatency(q.Corrected),
		Status:    status,
		Errors:    errCounts(q.Errors),
		BytesIn:   q.BytesIn,
		BytesOut:  q.BytesOut,
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
