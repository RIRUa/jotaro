package report

import (
	"encoding/json"
	"fmt"
	"github.com/RIRUa/jotaro/internal/histogram"
	"github.com/RIRUa/jotaro/record"
	"io"
	"maps"
	"time"
)

// JSONVersion is the version of the schema that WriteJSON emits. It is raised on every change that breaks compatibility.
// ReadJSON can read every version from 1 on.
//
//	3: added series (per-second time series) and, in requests, dns / connect / tls / conn_new / conn_reused.
//	   conn_new / conn_reused / dns / connect / tls in series, and health (health of the load generator), were added later
//	   (JSON without them is read as 0 / empty)
//	2: added started_at and the buckets (histogram) of latency
//	1: first version
const JSONVersion = 3

// Types for output. They are kept separate so the JSON shape does not change when Report's internals do.
// All durations are integer nanoseconds (field names end in _ns).

// jsonLatency is the JSON form of Latency.
type jsonLatency struct {
	Count  int   `json:"count"`
	MinNS  int64 `json:"min_ns"`
	MeanNS int64 `json:"mean_ns"`
	P50NS  int64 `json:"p50_ns"`
	P90NS  int64 `json:"p90_ns"`
	P95NS  int64 `json:"p95_ns"`
	P99NS  int64 `json:"p99_ns"`
	MaxNS  int64 `json:"max_ns"`
	// Buckets is the log histogram (bucket number → count). It is used to recompute percentiles when merging.
	Buckets map[int]int `json:"buckets"`
}

// jsonRequests is the JSON form of RequestStats.
type jsonRequests struct {
	Count     int         `json:"count"`
	Latency   jsonLatency `json:"latency"`
	TTFB      jsonLatency `json:"ttfb"`
	Corrected jsonLatency `json:"corrected"`
	DNS       jsonLatency `json:"dns"`
	Connect   jsonLatency `json:"connect"`
	TLS       jsonLatency `json:"tls"`
	// ConnNew / ConnReused are the number of requests that opened a new connection / reused one.
	ConnNew    int            `json:"conn_new"`
	ConnReused int            `json:"conn_reused"`
	Status     map[string]int `json:"status"`
	Errors     map[string]int `json:"errors"`
	BytesIn    int64          `json:"bytes_in"`
	BytesOut   int64          `json:"bytes_out"`
}

// jsonStep is the JSON form of StepStats.
type jsonStep struct {
	Name     string         `json:"name"`
	Count    int            `json:"count"`
	Failed   int            `json:"failed"`
	Duration jsonLatency    `json:"duration"`
	Errors   map[string]int `json:"errors"`
	Requests jsonRequests   `json:"requests"`
}

// jsonIterations is the JSON form of IterationStats.
type jsonIterations struct {
	Count  int            `json:"count"`
	Failed int            `json:"failed"`
	Active jsonLatency    `json:"active"` // time without Think
	Total  jsonLatency    `json:"total"`  // time including Think
	Errors map[string]int `json:"errors"`
}

// jsonScenario is the JSON form of ScenarioReport.
type jsonScenario struct {
	Name       string         `json:"name"`
	Iterations jsonIterations `json:"iterations"`
	Steps      []jsonStep     `json:"steps"`
}

// jsonSeriesPoint is the JSON form of SeriesPoint.
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

// jsonHealthPoint is the JSON form of record.HealthPoint.
type jsonHealthPoint struct {
	Second     int     `json:"second"`
	Goroutines int     `json:"goroutines"`
	CPU        float64 `json:"cpu"` // 0 to 1; -1 when it could not be obtained
}

// jsonReport is the top level of the JSON output, and the only thing ReadJSON decodes into.
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

// WriteJSON writes the report to w as JSON. Durations are integer nanoseconds.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r.toJSON())
}

// toJSON converts the report to its JSON form. Slices that would be nil are left empty here;
// ReadJSON turns empty values back into the zero values of Report.
func (r *Report) toJSON() jsonReport {
	out := jsonReport{
		Version:   JSONVersion,
		Open:      r.Open,
		Dropped:   r.Dropped,
		StartedAt: r.StartedAt,
		ElapsedNS: ns(r.Elapsed),
		RPS:       r.RPS(),
		Requests:  toJSONRequests(r.Requests),
		Scenarios: []jsonScenario{}, // use [] instead of null
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

// toJSONRequests converts RequestStats. It always sets the maps, so JSON shows {} instead of null.
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

// toJSONLatency converts Latency, including the histogram buckets that MergeReports needs later.
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
		// use {} instead of null
		Buckets: maps.Clone(bucketsOrEmpty(l.hist)),
	}
}

// errCounts always returns a map, even when empty, so that nil also becomes {}.
func errCounts(m map[record.ErrKind]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

// ns is d as integer nanoseconds, the unit of every duration in the JSON.
func ns(d time.Duration) int64 { return d.Nanoseconds() }

// ReadJSON turns JSON written by WriteJSON back into a Report.
// It returns an error when the version is not supported, or when other data follows the JSON.
// The latter is the case when raw data (JSON Lines, the output of record.WriteRaw) is passed by mistake.
func ReadJSON(r io.Reader) (*Report, error) {
	var in jsonReport
	dec := json.NewDecoder(r)
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("jotaro: read report: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("jotaro: read report: other data follows the JSON (for raw data, use record.ReadRaw / -raw)")
	}
	if in.Version < 1 || in.Version > JSONVersion {
		return nil, fmt.Errorf("jotaro: unsupported report version %d (want 1..%d)", in.Version, JSONVersion)
	}
	return in.toReport(), nil
}

// toReport is the inverse of toJSON. Empty maps and slices go back to nil (the same shape as the result of BuildReport).
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
		rep.Health = append(rep.Health, record.HealthPoint{Second: p.Second, Goroutines: p.Goroutines, CPU: p.CPU})
	}
	return rep
}

// fromJSONRequests is the inverse of toJSONRequests. Empty maps become nil, as in a fresh aggregate.
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

// fromJSONLatency is the inverse of toJSONLatency.
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

// fromErrCounts is the inverse of errCounts: an empty map becomes nil.
func fromErrCounts(m map[string]int) map[record.ErrKind]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[record.ErrKind]int, len(m))
	for k, v := range m {
		out[record.ErrKind(k)] = v
	}
	return out
}

// bucketsOrEmpty returns an empty map even for nil.
func bucketsOrEmpty(h histogram.H) map[int]int {
	if h == nil {
		return map[int]int{}
	}
	return h
}

// nilIfEmpty is the inverse of bucketsOrEmpty: an empty map becomes a nil histogram.
func nilIfEmpty(m map[int]int) histogram.H {
	if len(m) == 0 {
		return nil
	}
	return m
}
