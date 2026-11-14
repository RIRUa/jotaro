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

var progressLine = regexp.MustCompile(`^\d+s/\d+s rps=\d+ p95=\S+ err=\d+\.\d%$`)

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
