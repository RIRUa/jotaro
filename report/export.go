package report

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// vegetaResult is one line of vegeta's JSON result encoding, so that "vegeta report -type=json"
// and "vegeta plot" can read it. Durations are integer nanoseconds.
type vegetaResult struct {
	Attack    string      `json:"attack"`
	Seq       uint64      `json:"seq"`
	Code      uint16      `json:"code"`
	Timestamp time.Time   `json:"timestamp"`
	Latency   int64       `json:"latency"`
	BytesOut  uint64      `json:"bytes_out"`
	BytesIn   uint64      `json:"bytes_in"`
	Error     string      `json:"error"`
	Body      []byte      `json:"body"`
	Method    string      `json:"method"`
	URL       string      `json:"url"`
	Headers   interface{} `json:"headers"`
}

// WriteVegeta writes the requests of raw as vegeta results, one JSON object per line, ordered by start time.
// The attack name is "<scenario>/<step>". Scenario and step details beyond that are lost,
// which is a limit of the vegeta format.
// A network error has code 0 and the error kind as its message. HTTP 4xx/5xx keep their status code.
func WriteVegeta(w io.Writer, raw *record.Raw) error {
	reqs := slices.Clone(raw.Requests)
	slices.SortStableFunc(reqs, func(a, b record.Record) int { return a.Start.Compare(b.Start) })
	enc := json.NewEncoder(w)
	for i, r := range reqs {
		err := enc.Encode(vegetaResult{
			Attack:    r.Scenario + "/" + r.Step,
			Seq:       uint64(i),
			Code:      uint16(r.Code),
			Timestamp: r.Start,
			Latency:   r.Latency.Nanoseconds(),
			BytesOut:  uint64(r.BytesOut),
			BytesIn:   uint64(r.BytesIn),
			Error:     string(r.Err),
			Method:    r.Method,
			URL:       r.URL,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// WriteSeriesCSV writes the per-second time series of the report as CSV, with a header row.
// Durations are integer milliseconds with a fraction (for example 12.345), so spreadsheets can use them directly.
func (r *Report) WriteSeriesCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"second", "requests", "errors", "http_5xx", "new_conns", "reused_conns", "concurrency",
		"latency_min_ms", "latency_mean_ms", "latency_p50_ms", "latency_p90_ms", "latency_p95_ms", "latency_p99_ms", "latency_max_ms",
	}); err != nil {
		return err
	}
	ms := func(d time.Duration) string {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 3, 64)
	}
	num := strconv.Itoa
	for _, p := range r.Series {
		l := p.Latency
		if err := cw.Write([]string{
			num(p.Second), num(p.Requests), num(p.Errors), num(p.HTTP5xx), num(p.NewConns), num(p.ReusedConns), num(p.Concurrency),
			ms(l.Min), ms(l.Mean), ms(l.P50), ms(l.P90), ms(l.P95), ms(l.P99), ms(l.Max),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
