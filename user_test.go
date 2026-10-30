package jotaro

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func testUser(t *testing.T, baseURL string, id, iter int, col *record.Collector) *User {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &User{
		ID:        id,
		Iteration: iter,
		Client:    &http.Client{Jar: jar},
		ctx:       context.Background(),
		scenario:  "checkout",
		baseURL:   baseURL,
		col:       col,
	}
}

func TestStepRecordsUnderStepName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	col := record.NewCollector()
	u := testUser(t, srv.URL, 4, 2, col)
	sc := NewScenario("checkout", func(ctx context.Context, u *User) error {
		if err := u.Step("login", func(s *Step) error {
			return s.Get("/login").Do().Err()
		}); err != nil {
			return err
		}
		return u.Step("list", func(s *Step) error {
			if s.Name() != "list" {
				t.Errorf("Name = %q", s.Name())
			}
			return s.Get("/items").Do().Err()
		})
	})
	if err := sc.run(u); err != nil {
		t.Fatal(err)
	}

	reqs, steps, iters := col.Snapshot()
	if len(reqs) != 2 || len(steps) != 2 || len(iters) != 1 {
		t.Fatalf("got %d/%d/%d, want 2/2/1", len(reqs), len(steps), len(iters))
	}
	if reqs[0].Step != "login" || reqs[1].Step != "list" {
		t.Fatalf("Step = %q, %q", reqs[0].Step, reqs[1].Step)
	}
	for _, r := range reqs {
		if r.Scenario != "checkout" || r.UserID != 4 || r.Iteration != 2 {
			t.Fatalf("tag = %+v", r)
		}
	}
	if steps[0].Step != "login" || steps[1].Step != "list" || steps[0].Err != record.ErrNone {
		t.Fatalf("steps = %+v", steps)
	}
	if iters[0].Err != record.ErrNone || iters[0].UserID != 4 {
		t.Fatalf("iter = %+v", iters[0])
	}
}

func TestStepErrorIsReturnedAndRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	col := record.NewCollector()
	u := testUser(t, srv.URL, 0, 0, col)
	sc := NewScenario("checkout", func(ctx context.Context, u *User) error {
		return u.Step("order", func(s *Step) error {
			return s.Post("/orders").Do().ExpectStatus(201)
		})
	})
	err := sc.run(u)
	var ee *ExpectError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v", err)
	}

	reqs, steps, iters := col.Snapshot()
	if reqs[0].Code != 500 || reqs[0].Err != record.ErrNone {
		t.Fatalf("request = %+v", reqs[0])
	}
	if steps[0].Err != record.ErrExpect || iters[0].Err != record.ErrExpect {
		t.Fatalf("step=%q iter=%q", steps[0].Err, iters[0].Err)
	}
}

func TestThinkExcludedFromActive(t *testing.T) {
	col := record.NewCollector()
	u := testUser(t, "http://unused", 0, 0, col)
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		u.Think(80 * time.Millisecond)
		return nil
	})
	sc.run(u)

	_, _, iters := col.Snapshot()
	it := iters[0]
	if it.Think < 80*time.Millisecond {
		t.Fatalf("Think = %v", it.Think)
	}
	if it.Duration < it.Think || it.Active() >= 40*time.Millisecond {
		t.Fatalf("Duration=%v Think=%v Active=%v", it.Duration, it.Think, it.Active())
	}
}

func TestThinkReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	u := testUser(t, "http://unused", 0, 0, record.NewCollector())
	u.ctx = ctx
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	u.Think(5 * time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("did not return on ctx cancellation")
	}
}

func TestPanicBecomesIterationFailure(t *testing.T) {
	col := record.NewCollector()
	u := testUser(t, "http://unused", 0, 0, col)
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		panic("boom")
	})
	if err := sc.run(u); err == nil {
		t.Fatal("a panic should come back as an error")
	}
	_, _, iters := col.Snapshot()
	if len(iters) != 1 || iters[0].Err != record.ErrOther {
		t.Fatalf("iters = %+v", iters)
	}
}

func TestCookiesAreKeptWithinUserOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/"})
		case "/me":
			if _, err := r.Cookie("sid"); err != nil {
				w.WriteHeader(401)
			}
		}
	}))
	defer srv.Close()

	col := record.NewCollector()
	u1 := testUser(t, srv.URL, 1, 0, col)
	u2 := testUser(t, srv.URL, 2, 0, col)

	me := func(u *User) int {
		var code int
		u.Step("me", func(s *Step) error {
			code = s.Get("/me").Do().Code
			return nil
		})
		return code
	}

	u1.Step("login", func(s *Step) error { return s.Get("/login").Do().Err() })
	if got := me(u1); got != 200 {
		t.Fatalf("/me of a logged-in user = %d, want 200", got)
	}
	if got := me(u2); got != 401 {
		t.Fatalf("/me of another user = %d, want 401", got)
	}
}
