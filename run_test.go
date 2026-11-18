package jotaro

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

func TestRunValidate(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error { return nil })
	cases := map[string]Plan{
		"no scenario": {Users: 1, Duration: time.Second},
		"no Fn":       {Scenario: &Scenario{Name: "s"}, Users: 1, Duration: time.Second},
		"Users is 0":  {Scenario: sc, Duration: time.Second},
		"no Duration": {Scenario: sc, Users: 1},
	}
	for name, p := range cases {
		if _, err := Run(context.Background(), p); err == nil {
			t.Errorf("%s: should be an error", name)
		}
	}
}

func TestRunUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sc := NewScenario("checkout", func(ctx context.Context, u *User) error {
		if err := u.Step("login", func(s *Step) error {
			return s.Post("/login").Do().ExpectStatus(200)
		}); err != nil {
			return err
		}
		u.Think(5 * time.Millisecond)
		return u.Step("list", func(s *Step) error {
			return s.Get("/items").Do().ExpectStatus(200)
		})
	})

	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Users: 3, Duration: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Scenarios) != 1 {
		t.Fatalf("Scenarios = %d", len(rep.Scenarios))
	}
	s := rep.Scenarios[0]
	if s.Iterations.Count < 3 || s.Iterations.Failed != 0 {
		t.Fatalf("Iterations = %+v", s.Iterations)
	}
	if len(s.Steps) != 2 || s.Steps[0].Name != "login" || s.Steps[1].Name != "list" {
		t.Fatalf("Steps = %+v", s.Steps)
	}
	for _, st := range s.Steps {
		if st.Count != s.Iterations.Count || st.Failed != 0 {
			t.Errorf("%s: Count=%d Failed=%d, iterations=%d", st.Name, st.Count, st.Failed, s.Iterations.Count)
		}
	}
	if rep.Requests.Count != 2*s.Iterations.Count || rep.Requests.Status["2xx"] != rep.Requests.Count {
		t.Fatalf("Requests = %+v", rep.Requests)
	}
	if rep.Elapsed < 300*time.Millisecond || rep.RPS() <= 0 {
		t.Fatalf("Elapsed=%v RPS=%v", rep.Elapsed, rep.RPS())
	}
	// Scenario time without Think is shorter than the time with it.
	if s.Iterations.Active.P50 >= s.Iterations.Total.P50 {
		t.Fatalf("Active=%v Total=%v", s.Iterations.Active.P50, s.Iterations.Total.P50)
	}
}

func TestRunAllUsersParticipate(t *testing.T) {
	var seen [4]atomic.Bool
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		seen[u.ID].Store(true)
		u.Think(10 * time.Millisecond)
		return nil
	})
	if _, err := Run(context.Background(), Plan{Scenario: sc, Users: 4, Duration: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	for id := range seen {
		if !seen[id].Load() {
			t.Errorf("User %d never ran", id)
		}
	}
}

// Iterations still running after Duration are waited for until they finish.
func TestRunLetsInflightIterationFinish(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		u.Think(300 * time.Millisecond)
		return nil
	})
	rep, err := Run(context.Background(), Plan{Scenario: sc, Users: 1, Duration: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	it := rep.Scenarios[0].Iterations
	if it.Count != 1 || it.Failed != 0 {
		t.Fatalf("Iterations = %+v", it)
	}
	if rep.Elapsed < 300*time.Millisecond {
		t.Fatalf("Elapsed = %v: did not wait for running iterations", rep.Elapsed)
	}
}

// Past Grace, they are aborted and recorded as record.ErrCanceled.
func TestRunAbortsAfterGrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("hang", func(s *Step) error { return s.Get("/").Do().Err() })
	})

	start := time.Now()
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Users: 2,
		Duration: 50 * time.Millisecond, Grace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("not aborted; took %v", took)
	}
	it := rep.Scenarios[0].Iterations
	if it.Failed != it.Count || it.Errors[record.ErrCanceled] != it.Count {
		t.Fatalf("Iterations = %+v", it)
	}
}

func TestRunStopsOnParentCancel(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		u.Think(10 * time.Second)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := Run(ctx, Plan{Scenario: sc, Users: 2, Duration: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("did not stop on cancellation of the parent ctx: %v", took)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRunWritesRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	var buf bytes.Buffer
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Users: 2, Duration: 200 * time.Millisecond, Raw: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := record.ReadRaw(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Requests) != rep.Requests.Count || len(raw.Requests) == 0 {
		t.Fatalf("request count: raw = %d, rep = %d", len(raw.Requests), rep.Requests.Count)
	}
	// The result re-aggregated from raw data agrees with the result of Run. Run aggregates incrementally,
	// so the percentiles are within 1% of the exact ones that FromRaw computes; the rest is identical.
	again := report.FromRaw(raw)
	if again.Requests.Count != rep.Requests.Count || again.Elapsed != rep.Elapsed || again.Open != rep.Open ||
		again.Dropped != rep.Dropped || !reflect.DeepEqual(again.Health, rep.Health) {
		t.Errorf("the re-aggregated result differs from the result of Run:\n got %+v\nwant %+v", again, rep)
	}
	exact, approx := float64(again.Requests.Latency.P95), float64(rep.Requests.Latency.P95)
	if math.Abs(exact-approx) > 0.011*exact {
		t.Errorf("p95: exact %v, streamed %v (more than 1%% apart)", again.Requests.Latency.P95, rep.Requests.Latency.P95)
	}
}

func TestRunRawWriteErrorStillReturnsReport(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		u.Think(5 * time.Millisecond)
		return nil
	})
	rep, err := Run(context.Background(), Plan{Scenario: sc, Users: 1, Duration: 50 * time.Millisecond, Raw: failWriter{}})
	if err == nil {
		t.Error("a write failure did not become an error")
	}
	if rep == nil || len(rep.Scenarios) == 0 {
		t.Errorf("the aggregate should be returned even if writing fails: %+v", rep)
	}
}

// Run folds records into histograms with and without Plan.Raw. Both give the same counts.
func TestRunAggregatesWithAndWithoutRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	plan := Plan{Scenario: sc, BaseURL: srv.URL, Users: 3, Duration: 300 * time.Millisecond}

	streamed, err := Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Raw = &bytes.Buffer{}
	exact, err := Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	for name, rep := range map[string]*report.Report{"streamed": streamed, "with raw": exact} {
		if rep.Requests.Count == 0 || len(rep.Scenarios) != 1 || len(rep.Scenarios[0].Steps) != 1 {
			t.Fatalf("%s: unexpected report %+v", name, rep)
		}
		st := rep.Scenarios[0].Steps[0]
		if st.Count != rep.Requests.Count || st.Failed != 0 || rep.Scenarios[0].Iterations.Count != st.Count {
			t.Errorf("%s: step %+v, iterations %+v, requests %d", name, st, rep.Scenarios[0].Iterations, rep.Requests.Count)
		}
		if len(rep.Series) == 0 || len(rep.Health) == 0 || rep.StartedAt.IsZero() || rep.Elapsed <= 0 {
			t.Errorf("%s: series/health/start/elapsed missing: %+v", name, rep)
		}
		if rep.Series[0].Concurrency != 3 {
			t.Errorf("%s: concurrency in the first second = %d, want 3", name, rep.Series[0].Concurrency)
		}
		if l := rep.Requests.Latency; l.P50 < 2*time.Millisecond || l.P50 > l.Max || l.Min > l.P50 {
			t.Errorf("%s: latency %+v", name, l)
		}
	}
}

// The open model goes through the same path; dropped arrivals and the Open flag are kept.
func TestRunOpenStreamed(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		u.Think(50 * time.Millisecond)
		return nil
	})
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, Arrival: Rate(100, time.Second), MaxUsers: 1, Duration: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Open || rep.Dropped == 0 || rep.Scenarios[0].Iterations.Count == 0 {
		t.Errorf("Open = %v, Dropped = %d, iterations = %d", rep.Open, rep.Dropped, rep.Scenarios[0].Iterations.Count)
	}
	if got := rep.Series[0].Concurrency; got != 1 {
		t.Errorf("concurrency = %d, want 1 (MaxUsers)", got)
	}
}

func TestPlanScenariosValidate(t *testing.T) {
	ok := NewScenario("a", func(ctx context.Context, u *User) error { return nil })
	dup := NewScenario("a", func(ctx context.Context, u *User) error { return nil })
	cases := map[string]Plan{
		"both set":    {Scenario: ok, Scenarios: []Weighted{{ok, 1}}, Users: 1, Duration: time.Second},
		"empty":       {Scenarios: []Weighted{}, Users: 1, Duration: time.Second},
		"nil":         {Scenarios: []Weighted{{nil, 1}}, Users: 1, Duration: time.Second},
		"zero weight": {Scenarios: []Weighted{{ok, 0}}, Users: 1, Duration: time.Second},
		"dup name":    {Scenarios: []Weighted{{ok, 1}, {dup, 1}}, Users: 1, Duration: time.Second},
	}
	for name, p := range cases {
		if err := p.validate(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestPlanScenariosMix(t *testing.T) {
	noop := func(ctx context.Context, u *User) error { return nil }
	mix := []Weighted{{NewScenario("browse", noop), 3}, {NewScenario("checkout", noop), 1}}
	for name, p := range map[string]Plan{
		"closed": {Scenarios: mix, Users: 4, Duration: 300 * time.Millisecond},
		"open":   {Scenarios: mix, Arrival: Rate(1000, time.Second), Duration: 300 * time.Millisecond},
	} {
		rep, err := Run(context.Background(), p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		counts := map[string]int{}
		for _, s := range rep.Scenarios {
			counts[s.Name] = s.Iterations.Count
		}
		b, c := counts["browse"], counts["checkout"]
		if b == 0 || c == 0 || b < c {
			t.Errorf("%s: browse=%d checkout=%d, want both run and browse more often", name, b, c)
		}
	}
}

type wrapTransport struct{}

func (wrapTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("wrapper must not be used by Run")
}

// A replaced http.DefaultTransport that is not an *http.Transport must not make Run panic,
// and Run must not use it either: it builds its own transport.
// It replaces the global http.DefaultTransport, so it must not become t.Parallel.
func TestRunDefaultTransportNotHTTPTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// Cleanups run last-in first-out: DefaultTransport is restored before the server closes.
	t.Cleanup(srv.Close)
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	for name, rt := range map[string]http.RoundTripper{
		"wrapper":   wrapTransport{},
		"nil":       nil,
		"typed nil": (*http.Transport)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			http.DefaultTransport = rt
			sc := NewScenario("s", func(ctx context.Context, u *User) error {
				return u.Step("get", func(s *Step) error {
					return s.Get("/").Do().ExpectStatus(200)
				})
			})
			rep, err := Run(context.Background(), Plan{Scenario: sc, BaseURL: srv.URL, Users: 1, Duration: 100 * time.Millisecond})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if rep.Requests.Count == 0 || len(rep.Requests.Errors) != 0 {
				t.Errorf("requests=%d errors=%v, want requests>0 and no errors", rep.Requests.Count, rep.Requests.Errors)
			}
		})
	}
}
