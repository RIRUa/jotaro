package jotaro

import (
	"bytes"
	"context"
	"errors"
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
	// The result re-aggregated from raw data matches the result of Run.
	if again := report.FromRaw(raw); !reflect.DeepEqual(again, rep) {
		t.Errorf("the re-aggregated result differs from the result of Run:\n got %+v\nwant %+v", again, rep)
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
