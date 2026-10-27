package jotaro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateValidate(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error { return nil })
	cases := map[string]Plan{
		"Users と併用":   {Scenario: sc, Users: 1, Arrival: Rate(1, time.Second), Duration: time.Second},
		"回数が 0":       {Scenario: sc, Arrival: Rate(0, time.Second), Duration: time.Second},
		"期間が 0":       {Scenario: sc, Arrival: Rate(1, 0), Duration: time.Second},
		"MaxUsers が負": {Scenario: sc, Arrival: Rate(1, time.Second), MaxUsers: -1, Duration: time.Second},
	}
	for name, p := range cases {
		if _, err := Run(context.Background(), p); err == nil {
			t.Errorf("%s: エラーになるはず", name)
		}
	}
}

func TestRunRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Arrival: Rate(100, time.Second), Duration: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Open || rep.Dropped != 0 {
		t.Fatalf("Open=%v Dropped=%d", rep.Open, rep.Dropped)
	}
	// 100/s × 0.5s = 50 回。タイマーの揺れを見込んで幅を持たせる。
	if n := rep.Scenarios[0].Iterations.Count; n < 40 || n > 51 {
		t.Fatalf("Iterations = %d, 約 50 のはず", n)
	}
	if rep.Scenarios[0].Iterations.Failed != 0 {
		t.Fatalf("Failed = %d", rep.Scenarios[0].Iterations.Failed)
	}
}

// 応答が遅くて同時実行数の上限に達したら、到着はドロップされる。
func TestRunRateDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Arrival: Rate(50, time.Second),
		MaxUsers: 1, Duration: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped == 0 {
		t.Fatal("ドロップが記録されるはず")
	}
	if n := rep.Scenarios[0].Iterations.Count; n > 3 {
		t.Fatalf("Iterations = %d, MaxUsers=1 なら 2 回程度のはず", n)
	}
}

// 予定到着時刻は、イテレーションの最初のリクエストだけに付く。
func TestRunRateScheduled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		for _, name := range []string{"a", "b"} {
			if err := u.Step(name, func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) }); err != nil {
				return err
			}
			u.Think(20 * time.Millisecond)
		}
		return nil
	})
	p := Plan{Scenario: sc, BaseURL: srv.URL, Arrival: Rate(20, time.Second), Duration: 200 * time.Millisecond}
	ctx := context.Background()
	r := &runner{p: p, tr: http.DefaultTransport, timeout: time.Second, col: NewCollector(), stopCtx: ctx, runCtx: ctx}
	stopCtx, stop := context.WithTimeout(ctx, p.Duration)
	defer stop()
	r.stopCtx = stopCtx
	r.runOpen()

	reqs, _, _ := r.col.Snapshot()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for _, rec := range reqs {
		switch rec.Step {
		case "a": // 予定より遅れていれば Start より前、遅れなければ同じか少し前。
			if rec.Scheduled.After(rec.Start) {
				t.Errorf("a: Scheduled が Start より後: %v", rec.Scheduled.Sub(rec.Start))
			}
		case "b":
			if !rec.Scheduled.Equal(rec.Start) {
				t.Errorf("b: Scheduled = Start のはず")
			}
		}
	}
}

func TestRecordCorrected(t *testing.T) {
	now := time.Now()
	r := Record{Scheduled: now, Start: now.Add(300 * time.Millisecond), Latency: 50 * time.Millisecond}
	if got := r.Corrected(); got != 350*time.Millisecond {
		t.Fatalf("Corrected = %v", got)
	}
	if got := (Record{Latency: 5}).Corrected(); got != 5 {
		t.Fatalf("Scheduled 未設定のとき Latency を返すはず: %v", got)
	}
}
