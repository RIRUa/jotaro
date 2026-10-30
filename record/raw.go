package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// RawVersion is the version of the raw data format that WriteRaw emits. It is raised on every change that breaks compatibility.
// ReadRaw can read every version from 1 on.
const RawVersion = 1

// Raw is the raw data of a run. It holds the records of requests, steps and iterations before aggregation.
//
// An aggregated Report keeps its size however many requests there are, while raw data grows in proportion to the count.
// Use it to re-aggregate from another angle later, or to visualize with other tools.
type Raw struct {
	// Open reports whether the run used Arrival (the open model).
	Open bool
	// Dropped is the number of arrivals that were not run because of the concurrency cap.
	Dropped int
	// StartedAt is when the run started. Zero if unknown.
	StartedAt time.Time
	// Elapsed is the run time. If 0, it is derived from the records (same as BuildReport).
	Elapsed time.Duration

	Requests   []Record
	Steps      []StepRecord
	Iterations []IterationRecord
	// Health is the health of the load generator. nil if there are no records.
	Health []HealthPoint
}

// 出力用の型。1 行に 1 つ、type で種類を分ける。
// 時間は集計の JSON と同じくナノ秒の整数（フィールド名は _ns で終える）。
// 0 や空のものは省く（読むときは 0・空に戻る）。

// rawMeta is the first line of the stream. It carries the run-wide fields of Raw
// and the format version, which ReadRaw checks before trusting the rest.
type rawMeta struct {
	Type      string     `json:"type"` // "meta"
	Version   int        `json:"version"`
	Open      bool       `json:"open"`
	Dropped   int        `json:"dropped"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	ElapsedNS int64      `json:"elapsed_ns"`
}

// rawRequest is the line form of Record. Durations are nanoseconds (the _ns fields),
// and optional fields are left out when zero to keep large files small.
type rawRequest struct {
	Type      string     `json:"type"` // "request"
	Scenario  string     `json:"scenario"`
	Step      string     `json:"step"`
	UserID    int        `json:"user_id"`
	Iteration int        `json:"iteration"`
	Method    string     `json:"method"`
	URL       string     `json:"url"`
	Code      int        `json:"code,omitempty"`
	Err       string     `json:"err,omitempty"`
	Scheduled *time.Time `json:"scheduled,omitempty"`
	Start     time.Time  `json:"start"`
	LatencyNS int64      `json:"latency_ns"`
	Conn      string     `json:"conn,omitempty"`
	DNSNS     int64      `json:"dns_ns,omitempty"`
	ConnectNS int64      `json:"connect_ns,omitempty"`
	TLSNS     int64      `json:"tls_ns,omitempty"`
	TTFBNS    int64      `json:"ttfb_ns,omitempty"`
	BytesOut  int64      `json:"bytes_out,omitempty"`
	BytesIn   int64      `json:"bytes_in,omitempty"`
}

// rawStep is the line form of StepRecord.
type rawStep struct {
	Type       string    `json:"type"` // "step"
	Scenario   string    `json:"scenario"`
	Step       string    `json:"step"`
	UserID     int       `json:"user_id"`
	Iteration  int       `json:"iteration"`
	Start      time.Time `json:"start"`
	DurationNS int64     `json:"duration_ns"`
	Err        string    `json:"err,omitempty"`
}

// rawIteration is the line form of IterationRecord.
type rawIteration struct {
	Type       string    `json:"type"` // "iteration"
	Scenario   string    `json:"scenario"`
	UserID     int       `json:"user_id"`
	Iteration  int       `json:"iteration"`
	Start      time.Time `json:"start"`
	DurationNS int64     `json:"duration_ns"`
	ThinkNS    int64     `json:"think_ns,omitempty"`
	Err        string    `json:"err,omitempty"`
}

// rawHealth is the line form of HealthPoint. CPU is always written because -1 (unavailable) is meaningful.
type rawHealth struct {
	Type       string  `json:"type"` // "health"
	Second     int     `json:"second"`
	Goroutines int     `json:"goroutines"`
	CPU        float64 `json:"cpu"`
}

// 時刻は UTC の RFC 3339 で書く。zero は省く（ポインタにする）。
//
// utcPtr is for optional timestamps: nil makes omitempty drop the field.
func utcPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// utc converts a required timestamp to UTC. The zero time is kept as is so it round-trips unchanged.
func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

// derefTime is the reading counterpart of utcPtr: a missing field becomes the zero time.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ns は d をナノ秒の整数にする。
func ns(d time.Duration) int64 { return d.Nanoseconds() }

// WriteRaw writes the raw data to w as JSON Lines (one record per line).
//
// The first line holds information about the whole run (type "meta"), followed by request / step / iteration / health lines.
// It returns an error if writing fails; what was written until then stays in w.
func WriteRaw(w io.Writer, raw *Raw) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(rawMeta{
		Type: "meta", Version: RawVersion, Open: raw.Open, Dropped: raw.Dropped,
		StartedAt: utcPtr(raw.StartedAt), ElapsedNS: ns(raw.Elapsed),
	}); err != nil {
		return err
	}
	for _, r := range raw.Requests {
		if err := enc.Encode(rawRequest{
			Type: "request", Scenario: r.Scenario, Step: r.Step, UserID: r.UserID, Iteration: r.Iteration,
			Method: r.Method, URL: r.URL, Code: r.Code, Err: string(r.Err),
			Scheduled: utcPtr(r.Scheduled), Start: utc(r.Start), LatencyNS: ns(r.Latency), Conn: string(r.Conn),
			DNSNS: ns(r.DNS), ConnectNS: ns(r.Connect), TLSNS: ns(r.TLS), TTFBNS: ns(r.TTFB),
			BytesOut: r.BytesOut, BytesIn: r.BytesIn,
		}); err != nil {
			return err
		}
	}
	for _, s := range raw.Steps {
		if err := enc.Encode(rawStep{
			Type: "step", Scenario: s.Scenario, Step: s.Step, UserID: s.UserID, Iteration: s.Iteration,
			Start: utc(s.Start), DurationNS: ns(s.Duration), Err: string(s.Err),
		}); err != nil {
			return err
		}
	}
	for _, it := range raw.Iterations {
		if err := enc.Encode(rawIteration{
			Type: "iteration", Scenario: it.Scenario, UserID: it.UserID, Iteration: it.Iteration,
			Start: utc(it.Start), DurationNS: ns(it.Duration), ThinkNS: ns(it.Think), Err: string(it.Err),
		}); err != nil {
			return err
		}
	}
	for _, h := range raw.Health {
		if err := enc.Encode(rawHealth{Type: "health", Second: h.Second, Goroutines: h.Goroutines, CPU: h.CPU}); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ReadRaw reads JSON Lines written by WriteRaw.
//
// Lines of an unknown type are skipped (they are added by a newer version, and older readers must not break).
// It can read data without a meta line (the run time is then derived from the records).
func ReadRaw(r io.Reader) (*Raw, error) {
	dec := json.NewDecoder(bufio.NewReader(r))
	raw := &Raw{}
	for n := 1; ; n++ {
		var line json.RawMessage
		if err := dec.Decode(&line); err == io.EOF {
			return raw, nil
		} else if err != nil {
			return nil, fmt.Errorf("jotaro: read raw: record %d: %w", n, err)
		}
		if err := raw.addLine(line); err != nil {
			return nil, fmt.Errorf("jotaro: read raw: record %d: %w", n, err)
		}
	}
}

// addLine decodes one line and appends it to the matching slice of raw.
// It peeks at "type" first, and a meta line with an unsupported version is an error.
// An unknown type is ignored on purpose (see ReadRaw).
func (raw *Raw) addLine(line json.RawMessage) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return err
	}
	switch head.Type {
	case "meta":
		var m rawMeta
		if err := json.Unmarshal(line, &m); err != nil {
			return err
		}
		if m.Version < 1 || m.Version > RawVersion {
			return fmt.Errorf("unsupported raw version %d (want 1..%d)", m.Version, RawVersion)
		}
		raw.Open, raw.Dropped = m.Open, m.Dropped
		raw.StartedAt, raw.Elapsed = derefTime(m.StartedAt), time.Duration(m.ElapsedNS)
	case "request":
		var q rawRequest
		if err := json.Unmarshal(line, &q); err != nil {
			return err
		}
		raw.Requests = append(raw.Requests, Record{
			Scenario: q.Scenario, Step: q.Step, UserID: q.UserID, Iteration: q.Iteration,
			Method: q.Method, URL: q.URL, Code: q.Code, Err: ErrKind(q.Err),
			Scheduled: derefTime(q.Scheduled), Start: q.Start, Latency: time.Duration(q.LatencyNS), Conn: ConnKind(q.Conn),
			DNS: time.Duration(q.DNSNS), Connect: time.Duration(q.ConnectNS), TLS: time.Duration(q.TLSNS), TTFB: time.Duration(q.TTFBNS),
			BytesOut: q.BytesOut, BytesIn: q.BytesIn,
		})
	case "step":
		var s rawStep
		if err := json.Unmarshal(line, &s); err != nil {
			return err
		}
		raw.Steps = append(raw.Steps, StepRecord{
			Scenario: s.Scenario, Step: s.Step, UserID: s.UserID, Iteration: s.Iteration,
			Start: s.Start, Duration: time.Duration(s.DurationNS), Err: ErrKind(s.Err),
		})
	case "iteration":
		var it rawIteration
		if err := json.Unmarshal(line, &it); err != nil {
			return err
		}
		raw.Iterations = append(raw.Iterations, IterationRecord{
			Scenario: it.Scenario, UserID: it.UserID, Iteration: it.Iteration,
			Start: it.Start, Duration: time.Duration(it.DurationNS), Think: time.Duration(it.ThinkNS), Err: ErrKind(it.Err),
		})
	case "health":
		var h rawHealth
		if err := json.Unmarshal(line, &h); err != nil {
			return err
		}
		raw.Health = append(raw.Health, HealthPoint{Second: h.Second, Goroutines: h.Goroutines, CPU: h.CPU})
	}
	return nil
}
