// Package record は、負荷試験の生データ（リクエスト・ステップ・イテレーションの記録）と、その入出力。
package record

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

// ConnKind はリクエストが使った接続の由来。空は接続を得る前に失敗したもの。
type ConnKind string

const (
	ConnNew    ConnKind = "new"    // 新しく張った接続
	ConnReused ConnKind = "reused" // 接続プールの再利用
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

	// Conn は使った接続が新規か再利用か。
	Conn ConnKind

	// httptrace で取れる各区間。取れなかった区間は 0。
	// 再利用した接続では DNS / Connect / TLS はどれも 0 になる。
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

// Take は記録を渡して Collector を空にする。Snapshot と違い、コピーしない。
// 全部の記録を持つので、実行が終わって誰も Add しなくなってから呼ぶ。
func (c *Collector) Take() (requests []Record, steps []StepRecord, iterations []IterationRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests, steps, iterations = c.requests, c.steps, c.iterations
	c.requests, c.steps, c.iterations = nil, nil, nil
	return requests, steps, iterations
}

// HealthPoint は負荷生成側（jotaro を動かしているプロセス）の、1 秒ごとの健全性。
// CPU が上限に近いときは、生成側が詰まって測定値が実際より悪く出ている疑いがある。
type HealthPoint struct {
	// Second は実行の開始からの経過秒（0 始まり）。区間は [Second, Second+1) 秒。
	Second int
	// Goroutines は区間の終わりの時点のゴルーチン数。
	Goroutines int
	// CPU は区間の間にプロセスが使った CPU 時間の割合（0〜1）。GOMAXPROCS 個の CPU 全体に対する割合。
	// 負荷をかける対象を同じプロセスで動かしているときは、その分も含む。
	// CPU 時間を取得できない環境（Unix 以外）では -1。
	CPU float64
}
