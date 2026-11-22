package jotaro

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// syncBuffer is a bytes.Buffer that the progress goroutine and the test can use at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFormatProgress(t *testing.T) {
	prev := progressStats{count: 100, failed: 0}
	cur := progressStats{count: 580, failed: 1, p95: 38400 * time.Microsecond}
	got := formatProgress(12*time.Second, 60*time.Second, prev, cur, time.Second)
	want := "12s/60s rps=480 p95=38.4ms err=0.2%"
	if got != want {
		t.Errorf("formatProgress = %q, want %q", got, want)
	}

	// Nothing has finished yet: no division by zero, no NaN.
	got = formatProgress(time.Second, time.Minute, progressStats{}, progressStats{}, time.Second)
	want = "1s/60s rps=0 p95=0s err=0.0%"
	if got != want {
		t.Errorf("formatProgress (empty) = %q, want %q", got, want)
	}
}

var progressLine = regexp.MustCompile(`^\d+s/\d+s rps=\d+ p95=\S+ err=\d+\.\d%( 5xx=\d+\.\d%)?( fail=\d+\.\d%)?( dropped=\d+)?$`)

func TestRunProgress(t *testing.T) {
	old := progressInterval
	progressInterval = 30 * time.Millisecond
	defer func() { progressInterval = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})

	for name, raw := range map[string]*bytes.Buffer{"streaming": nil, "with Raw": {}} {
		t.Run(name, func(t *testing.T) {
			var out syncBuffer
			p := Plan{Scenario: sc, BaseURL: srv.URL, Users: 2, Duration: 300 * time.Millisecond, Progress: &out}
			if raw != nil {
				p.Raw = raw
			}
			rep, err := Run(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Requests.Count == 0 {
				t.Fatal("no requests were made")
			}
			if raw != nil && raw.Len() == 0 {
				t.Error("Raw was not written when Progress is set")
			}

			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) < 3 {
				t.Fatalf("got %d progress lines, want at least 3: %q", len(lines), out.String())
			}
			for _, l := range lines {
				if !progressLine.MatchString(l) {
					t.Errorf("unexpected progress line %q", l)
				}
			}

			// Nothing may be written after Run has returned.
			n := len(out.String())
			time.Sleep(3 * progressInterval)
			if len(out.String()) != n {
				t.Error("progress was written after Run returned")
			}
		})
	}
}

func TestFormatProgressFields(t *testing.T) {
	cur := progressStats{
		count: 1000, failed: 2, http5xx: 310, iters: 500, itersFailed: 156, dropped: 7,
		p95: 38 * time.Millisecond,
	}
	got := formatProgress(12*time.Second, 60*time.Second, progressStats{}, cur, time.Second)
	want := "12s/60s rps=1000 p95=38ms err=0.2% 5xx=31.0% fail=31.2% dropped=7"
	if got != want {
		t.Errorf("formatProgress = %q, want %q", got, want)
	}

	// Each of the extra fields appears on its own, and only while it is not zero.
	for name, tc := range map[string]struct {
		cur  progressStats
		want string
	}{
		"5xx only":     {progressStats{count: 10, http5xx: 10}, "1s/60s rps=0 p95=0s err=0.0% 5xx=100.0%"},
		"fail only":    {progressStats{count: 10, iters: 4, itersFailed: 1}, "1s/60s rps=0 p95=0s err=0.0% fail=25.0%"},
		"dropped only": {progressStats{count: 10, iters: 4, dropped: 3}, "1s/60s rps=0 p95=0s err=0.0% dropped=3"},
		"none":         {progressStats{count: 10, iters: 4}, "1s/60s rps=0 p95=0s err=0.0%"},
	} {
		if got := formatProgress(time.Second, time.Minute, progressStats{}, tc.cur, 0); got != tc.want {
			t.Errorf("%s: formatProgress = %q, want %q", name, got, tc.want)
		}
	}
}

// TestProgressObserver checks what the progress counter takes from a record: a transport error counts only as an
// error, a response with a 5xx status counts only as 5xx, and a 4xx or a 2xx response counts as neither.
func TestProgressObserver(t *testing.T) {
	p := &progress{}
	p.ObserveRequest(record.Record{Code: 200})
	p.ObserveRequest(record.Record{Code: 404})
	p.ObserveRequest(record.Record{Code: 500})
	p.ObserveRequest(record.Record{Code: 503})
	p.ObserveRequest(record.Record{Code: 600})
	p.ObserveRequest(record.Record{Err: record.ErrTimeout})
	p.ObserveIteration(record.IterationRecord{})
	p.ObserveIteration(record.IterationRecord{Err: record.ErrExpect})
	got := p.stats()
	if got.count != 6 || got.failed != 1 || got.http5xx != 2 || got.iters != 2 || got.itersFailed != 1 {
		t.Errorf("stats = %+v", got)
	}
}

// runProgress runs sc against a server that answers with handler for a short time and returns the progress lines.
func runProgress(t *testing.T, handler http.HandlerFunc, mod func(*Plan), sc *Scenario) ([]string, *report.Report) {
	t.Helper()
	old := progressInterval
	progressInterval = 30 * time.Millisecond
	defer func() { progressInterval = old }()

	srv := httptest.NewServer(handler)
	defer srv.Close()
	var out syncBuffer
	p := Plan{Scenario: sc, BaseURL: srv.URL, Users: 2, Duration: 300 * time.Millisecond, Progress: &out}
	if mod != nil {
		mod(&p)
	}
	rep, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("got %d progress lines, want at least 3: %q", len(lines), out.String())
	}
	return lines, rep
}

// TestRunProgressShowsFailures is the case of issue 85: a server that answers every request with 500, or with a
// body that fails an Expect check, must not look healthy on the progress line.
func TestRunProgressShowsFailures(t *testing.T) {
	expect200 := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	last := func(lines []string) string { return lines[len(lines)-1] }

	t.Run("5xx", func(t *testing.T) {
		lines, rep := runProgress(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, nil, expect200)
		l := last(lines)
		if !strings.Contains(l, " err=0.0% ") || !strings.Contains(l, " 5xx=100.0%") || !strings.Contains(l, " fail=100.0%") {
			t.Errorf("last line = %q, want err=0.0%% 5xx=100.0%% fail=100.0%%", l)
		}
		if rep.Requests.Status["5xx"] != rep.Requests.Count {
			t.Errorf("report disagrees: %+v", rep.Requests)
		}
	})

	t.Run("expect mismatch", func(t *testing.T) {
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectBodyContains("ok") })
		})
		lines, _ := runProgress(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("nope")) }, nil, sc)
		l := last(lines)
		if strings.Contains(l, "5xx=") || !strings.Contains(l, " fail=100.0%") {
			t.Errorf("last line = %q, want fail=100.0%% and no 5xx", l)
		}
	})

	t.Run("healthy stays short", func(t *testing.T) {
		lines, _ := runProgress(t, func(w http.ResponseWriter, r *http.Request) {}, nil, expect200)
		for _, l := range lines {
			if strings.Contains(l, "5xx=") || strings.Contains(l, "fail=") || strings.Contains(l, "dropped=") {
				t.Errorf("healthy run shows an extra field: %q", l)
			}
		}
	})

	t.Run("dropped", func(t *testing.T) {
		slow := func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }
		lines, rep := runProgress(t, slow, func(p *Plan) {
			p.Users = 0
			p.Arrival = Rate(200, time.Second)
			p.MaxUsers = 1
		}, expect200)
		if rep.Dropped == 0 {
			t.Fatal("no arrival was dropped")
		}
		if l := last(lines); !strings.Contains(l, " dropped=") {
			t.Errorf("last line = %q, want dropped=N", l)
		}
	})
}

// TestRunProgressMatchesReport checks that routing records through Progress does not change the report.
func TestRunProgressMatchesReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	var out syncBuffer
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Users: 1, Duration: 100 * time.Millisecond, Progress: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Scenarios) != 1 || len(rep.Scenarios[0].Steps) != 1 {
		t.Fatalf("scenarios/steps missing: %+v", rep.Scenarios)
	}
	if rep.Scenarios[0].Iterations.Count == 0 || len(rep.Series) == 0 {
		t.Errorf("iterations or series missing: %d iterations, %d series points",
			rep.Scenarios[0].Iterations.Count, len(rep.Series))
	}
}
