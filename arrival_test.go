package jotaro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func TestRateValidate(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error { return nil })
	cases := map[string]Plan{
		"Users と併用":   {Scenario: sc, Users: 1, Arrival: Rate(1, time.Second), Duration: time.Second},
		"回数が 0":       {Scenario: sc, Arrival: Rate(0, time.Second), Duration: time.Second},
		"期間が 0":       {Scenario: sc, Arrival: Rate(1, 0), Duration: time.Second},
		"Ramp が両方 0":  {Scenario: sc, Arrival: Ramp(0, 0, time.Second, time.Second), Duration: time.Second},
		"Ramp の期間が 0": {Scenario: sc, Arrival: Ramp(0, 1, 0, time.Second), Duration: time.Second},
		"Ramp の傾斜が 0": {Scenario: sc, Arrival: Ramp(0, 1, time.Second, 0), Duration: time.Second},
		"Ramp が負":     {Scenario: sc, Arrival: Ramp(-1, 1, time.Second, time.Second), Duration: time.Second},
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
	// 100/s × 0.5s = 50 回。タイマーの揺れ（Windows では特に大きい）を見込んで幅を持たせる。
	if n := rep.Scenarios[0].Iterations.Count; n < 40 || n > 56 {
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
	r := &runner{p: p, tr: http.DefaultTransport, timeout: time.Second, col: record.NewCollector(), stopCtx: ctx, runCtx: ctx}
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
	r := record.Record{Scheduled: now, Start: now.Add(300 * time.Millisecond), Latency: 50 * time.Millisecond}
	if got := r.Corrected(); got != 350*time.Millisecond {
		t.Fatalf("Corrected = %v", got)
	}
	if got := (record.Record{Latency: 5}).Corrected(); got != 5 {
		t.Fatalf("Scheduled 未設定のとき Latency を返すはず: %v", got)
	}
}

func TestRampAt(t *testing.T) {
	near := func(got time.Duration, ok bool, want time.Duration) bool {
		return ok && (got-want).Abs() < time.Millisecond
	}
	// 0 → 10/s を 10 秒。N(t) = t²/2 なので、i 番目は sqrt(2i) 秒。区間の累積は 50 回。
	up := Ramp(0, 10, time.Second, 10*time.Second)
	for i, want := range map[int]time.Duration{0: 0, 2: 2 * time.Second, 8: 4 * time.Second, 50: 10 * time.Second, 60: 11 * time.Second} {
		if got, ok := up.at(i); !near(got, ok, want) {
			t.Errorf("up.at(%d) = %v, %v, want %v", i, got, ok, want)
		}
	}
	// 10 → 10/s は Rate と同じ。
	flat := Ramp(10, 10, time.Second, 10*time.Second)
	if got, ok := flat.at(25); !near(got, ok, 2500*time.Millisecond) {
		t.Errorf("flat.at(25) = %v, %v", got, ok)
	}
	// 10 → 0/s を 10 秒。累積 50 回のあとは到着しない。
	down := Ramp(10, 0, time.Second, 10*time.Second)
	if got, ok := down.at(50); !near(got, ok, 10*time.Second) {
		t.Errorf("down.at(50) = %v, %v", got, ok)
	}
	if _, ok := down.at(51); ok {
		t.Error("down.at(51) は到着なしのはず")
	}
	// 時刻は単調に増える。
	prev := time.Duration(-1)
	for i := 0; i < 200; i++ {
		got, _ := up.at(i)
		if got < prev {
			t.Fatalf("at(%d) = %v < %v", i, got, prev)
		}
		prev = got
	}
}

func TestRunRamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	// 0 → 200/s を 1 秒: 累積 100 回。
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: srv.URL, Arrival: Ramp(0, 200, time.Second, time.Second), Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := rep.Scenarios[0].Iterations.Count; n < 80 || n > 101 {
		t.Fatalf("Iterations = %d, 約 100 のはず", n)
	}
}

func TestStagesAt(t *testing.T) {
	// 0〜2 秒: 10/s (20 回)、2〜4 秒: 20/s (40 回)。
	s := Stages(
		Stage{Rate(10, time.Second), 2 * time.Second},
		Stage{Rate(20, time.Second), 2 * time.Second},
	)
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]time.Duration{0: 0, 19: 1900 * time.Millisecond, 20: 2 * time.Second, 22: 2100 * time.Millisecond, 59: 3950 * time.Millisecond} {
		if got, ok := s.at(i); !ok || got != want {
			t.Errorf("at(%d) = %v, %v, want %v", i, got, ok, want)
		}
	}
	if _, ok := s.at(60); ok {
		t.Error("全段階のあとは到着しないはず")
	}
}

func TestStagesRampHoldDown(t *testing.T) {
	// 0→10/s を 4 秒 (20 回)、10/s を 2 秒 (20 回)、10→0/s を 4 秒 (20 回)。
	s := Stages(
		Stage{Ramp(0, 10, time.Second, 4*time.Second), 4 * time.Second},
		Stage{Rate(10, time.Second), 2 * time.Second},
		Stage{Ramp(10, 0, time.Second, 4*time.Second), 4 * time.Second},
	)
	n := 0
	prev := time.Duration(-1)
	for ; ; n++ {
		got, ok := s.at(n)
		if !ok {
			break
		}
		if got < prev || got >= 10*time.Second {
			t.Fatalf("at(%d) = %v (前: %v)", n, got, prev)
		}
		prev = got
	}
	if n < 59 || n > 61 {
		t.Errorf("到着数 = %d, 約 60 のはず", n)
	}
}

func TestStagesValidate(t *testing.T) {
	bad := map[string]Arrival{
		"空":          Stages(),
		"For が 0":    Stages(Stage{Rate(1, time.Second), 0}),
		"Arrival なし": Stages(Stage{nil, time.Second}),
		"段階が不正":      Stages(Stage{Rate(0, time.Second), time.Second}),
	}
	for name, a := range bad {
		if a.validate() == nil {
			t.Errorf("%s: エラーになるはず", name)
		}
	}
}
