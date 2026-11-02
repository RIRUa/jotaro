// Package record holds the raw data of a load test (records of requests, steps and iterations), and its input and output.
package record

import (
	"sync"
	"time"
)

// ErrKind is the kind of a failure. The empty string means success.
type ErrKind string

const (
	ErrNone        ErrKind = ""                   // success
	ErrTimeout     ErrKind = "timeout"            // a deadline passed or the network timed out
	ErrConnRefused ErrKind = "connection_refused" // nothing was listening at the address
	ErrDNS         ErrKind = "dns"                // the host name could not be resolved
	ErrTLS         ErrKind = "tls"                // the handshake or certificate verification failed
	ErrCanceled    ErrKind = "canceled"           // the run was stopped while the request was in flight
	ErrExpect      ErrKind = "expect_mismatch"    // the response arrived but an Expect* check failed
	ErrOther       ErrKind = "other"              // any failure that fits none of the above
)

// ConnKind is where the connection used by a request came from. Empty means it failed before getting a connection.
type ConnKind string

const (
	ConnNew    ConnKind = "new"    // a newly opened connection
	ConnReused ConnKind = "reused" // reused from the connection pool
)

// Record is the raw data of one HTTP request. All aggregation is done from it.
type Record struct {
	Scenario  string
	Step      string
	UserID    int
	Iteration int

	Method string
	URL    string
	Code   int     // 0 means no response (network error)
	Err    ErrKind // kind of network error. HTTP 4xx/5xx are expressed by Code

	// Scheduled is the scheduled start time. In the Users model it equals Start.
	// In the Arrival model, only the first request of an iteration gets the scheduled arrival time.
	// It is used for Coordinated Omission correction.
	Scheduled time.Time
	Start     time.Time
	Latency   time.Duration

	// Conn is whether the connection used was new or reused.
	Conn ConnKind

	// Phases obtainable with httptrace. A phase that could not be obtained is 0.
	// With a reused connection, DNS / Connect / TLS are all 0.
	DNS     time.Duration
	Connect time.Duration
	TLS     time.Duration
	TTFB    time.Duration

	BytesOut int64
	BytesIn  int64
}

// Corrected is the time from the scheduled start until the response has been fully received.
// It includes the delay of a late start, which is what corrects for Coordinated Omission.
func (r Record) Corrected() time.Duration {
	if r.Scheduled.IsZero() {
		return r.Latency
	}
	return r.Start.Add(r.Latency).Sub(r.Scheduled)
}

// StepRecord is the record of one u.Step.
type StepRecord struct {
	Scenario  string
	Step      string
	UserID    int
	Iteration int

	Start    time.Time
	Duration time.Duration
	Err      ErrKind
}

// IterationRecord is the record of one run of a scenario.
type IterationRecord struct {
	Scenario  string
	UserID    int
	Iteration int

	Start    time.Time
	Duration time.Duration // includes Think
	Think    time.Duration
	Err      ErrKind
}

// Active returns the scenario time without Think.
func (r IterationRecord) Active() time.Duration {
	return r.Duration - r.Think
}

// Observer receives records as they are produced, without the Collector keeping them.
// Its methods are called from many goroutines at once, so an implementation must be safe for concurrent use.
type Observer interface {
	// ObserveRequest is called when a request has finished.
	ObserveRequest(Record)
	// ObserveStep is called when a step has finished.
	ObserveStep(StepRecord)
	// ObserveIteration is called when a scenario run has finished.
	ObserveIteration(IterationRecord)
	// ObserveIterationStart is called when a scenario run starts, with its start time.
	ObserveIterationStart(time.Time)
}

// Collector receives records concurrently.
//
// A Collector made with NewCollector keeps every record. One made with NewStreamingCollector
// hands them to an Observer and keeps nothing, so its memory does not grow with the number of requests.
type Collector struct {
	mu         sync.Mutex
	requests   []Record
	steps      []StepRecord
	iterations []IterationRecord
	dropped    int

	// obs, when not nil, receives the records instead of the slices above.
	obs Observer
}

// NewCollector returns an empty Collector ready for concurrent use. It keeps every record.
func NewCollector() *Collector {
	return &Collector{}
}

// NewStreamingCollector returns a Collector that passes each record to obs and does not keep it.
// Snapshot and Take return nothing for it. Only the count of dropped arrivals is kept.
func NewStreamingCollector(obs Observer) *Collector {
	return &Collector{obs: obs}
}

// AddRequest stores the record of one HTTP request. It is safe to call from many goroutines.
func (c *Collector) AddRequest(r Record) {
	if c.obs != nil {
		c.obs.ObserveRequest(r)
		return
	}
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.mu.Unlock()
}

// AddStep stores the record of one finished step. It is safe to call from many goroutines.
func (c *Collector) AddStep(r StepRecord) {
	if c.obs != nil {
		c.obs.ObserveStep(r)
		return
	}
	c.mu.Lock()
	c.steps = append(c.steps, r)
	c.mu.Unlock()
}

// AddIteration stores the record of one finished scenario run. It is safe to call from many goroutines.
func (c *Collector) AddIteration(r IterationRecord) {
	if c.obs != nil {
		c.obs.ObserveIteration(r)
		return
	}
	c.mu.Lock()
	c.iterations = append(c.iterations, r)
	c.mu.Unlock()
}

// StartIteration tells the Observer that a scenario run has started. It does nothing for a Collector that keeps records,
// because there the start time is already in the IterationRecord.
func (c *Collector) StartIteration(t time.Time) {
	if c.obs != nil {
		c.obs.ObserveIterationStart(t)
	}
}

// AddDropped counts one arrival that was not run because of the concurrency cap.
func (c *Collector) AddDropped() {
	c.mu.Lock()
	c.dropped++
	c.mu.Unlock()
}

// Dropped is the number of dropped arrivals.
func (c *Collector) Dropped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Snapshot returns a copy of the records at that moment.
func (c *Collector) Snapshot() (requests []Record, steps []StepRecord, iterations []IterationRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests = append(requests, c.requests...)
	steps = append(steps, c.steps...)
	iterations = append(iterations, c.iterations...)
	return requests, steps, iterations
}

// Take hands over the records and empties the Collector. Unlike Snapshot, it does not copy.
// Since it holds all the records, call it after the run is over and nobody calls Add any more.
func (c *Collector) Take() (requests []Record, steps []StepRecord, iterations []IterationRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests, steps, iterations = c.requests, c.steps, c.iterations
	c.requests, c.steps, c.iterations = nil, nil, nil
	return requests, steps, iterations
}

// HealthPoint is the per-second health of the load generator (the process running jotaro).
// When the CPU is close to its limit, the generator may be saturated and the measurements worse than reality.
type HealthPoint struct {
	// Second is the seconds elapsed since the start of the run (0-based). The interval is [Second, Second+1) seconds.
	Second int
	// Goroutines is the number of goroutines at the end of the interval.
	Goroutines int
	// CPU is the share of CPU time used by the process during the interval (0 to 1), relative to all GOMAXPROCS CPUs.
	// If the target under load runs in the same process, its share is included.
	// It is -1 on platforms where CPU time cannot be obtained (non-Unix).
	CPU float64
}
