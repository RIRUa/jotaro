package jotaro

import (
	"sync"
	"time"
)

// ErrKind は失敗の種類。空文字は成功を表す。
type ErrKind string

const (
	ErrNone        ErrKind = ""
	ErrTimeout     ErrKind = "timeout"
	ErrConnRefused ErrKind = "connection_refused"
	ErrDNS         ErrKind = "dns"
	ErrTLS         ErrKind = "tls"
	ErrCanceled    ErrKind = "canceled"
	ErrExpect      ErrKind = "expect_mismatch"
	ErrOther       ErrKind = "other"
)

// Record は 1 回の HTTP リクエストの生データ。集計はすべてここから行う。
type Record struct {
	Scenario  string
	Step      string
	UserID    int
	Iteration int

	Method string
	URL    string
	Code   int     // 0 は応答なし（通信エラー）
	Err    ErrKind // 通信エラーの種類。HTTP 4xx/5xx は Code で表す

	// Scheduled は予定開始時刻。Users モデルでは Start と同じ値になる。
	// Arrival モデルでは、イテレーションの最初のリクエストだけ予定到着時刻になる。
	// Coordinated Omission の補正に使う。
	Scheduled time.Time
	Start     time.Time
	Latency   time.Duration

	// httptrace で取れる各区間。取れなかった区間は 0。
	DNS     time.Duration
	Connect time.Duration
	TLS     time.Duration
	TTFB    time.Duration

	BytesOut int64
	BytesIn  int64
}

// Corrected は予定開始時刻から応答を受け取り終えるまでの時間。
// 予定より遅れて始まった分も含むので、Coordinated Omission の補正になる。
func (r Record) Corrected() time.Duration {
	if r.Scheduled.IsZero() {
		return r.Latency
	}
	return r.Start.Add(r.Latency).Sub(r.Scheduled)
}

// StepRecord は u.Step 1 回分の記録。
type StepRecord struct {
	Scenario  string
	Step      string
	UserID    int
	Iteration int

	Start    time.Time
	Duration time.Duration
	Err      ErrKind
}

// IterationRecord はシナリオ 1 回分の記録。
type IterationRecord struct {
	Scenario  string
	UserID    int
	Iteration int

	Start    time.Time
	Duration time.Duration // Think を含む
	Think    time.Duration
	Err      ErrKind
}

// Active は Think を除いたシナリオ時間を返す。
func (r IterationRecord) Active() time.Duration {
	return r.Duration - r.Think
}

// Collector は並行に記録を受け取る。
type Collector struct {
	mu         sync.Mutex
	requests   []Record
	steps      []StepRecord
	iterations []IterationRecord
	dropped    int
}

func NewCollector() *Collector {
	return &Collector{}
}

func (c *Collector) AddRequest(r Record) {
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.mu.Unlock()
}

func (c *Collector) AddStep(r StepRecord) {
	c.mu.Lock()
	c.steps = append(c.steps, r)
	c.mu.Unlock()
}

func (c *Collector) AddIteration(r IterationRecord) {
	c.mu.Lock()
	c.iterations = append(c.iterations, r)
	c.mu.Unlock()
}

// AddDropped は、同時実行数の上限のために実行しなかった到着を 1 回数える。
func (c *Collector) AddDropped() {
	c.mu.Lock()
	c.dropped++
	c.mu.Unlock()
}

// Dropped はドロップした到着の数。
func (c *Collector) Dropped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Snapshot はその時点の記録のコピーを返す。
func (c *Collector) Snapshot() (requests []Record, steps []StepRecord, iterations []IterationRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests = append(requests, c.requests...)
	steps = append(steps, c.steps...)
	iterations = append(iterations, c.iterations...)
	return requests, steps, iterations
}
