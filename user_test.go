package jotaro

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"runtime"
	"strings"
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
	inBubble(t, func(t *testing.T) {
		col := record.NewCollector()
		u := testUser(t, "http://unused", 0, 0, col)
		sc := NewScenario("s", func(ctx context.Context, u *User) error {
			u.Think(80 * time.Millisecond)
			time.Sleep(10 * time.Millisecond) // work that is not thinking
			return nil
		})
		sc.run(u)

		_, _, iters := col.Snapshot()
		it := iters[0]
		if it.Think != 80*time.Millisecond || it.Duration != 90*time.Millisecond || it.Active() != 10*time.Millisecond {
			t.Fatalf("Duration=%v Think=%v Active=%v, want 90ms, 80ms and 10ms", it.Duration, it.Think, it.Active())
		}
	})
}

func TestThinkReturnsOnCancel(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		u := testUser(t, "http://unused", 0, 0, record.NewCollector())
		u.ctx = ctx
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		u.Think(5 * time.Second)
		if got := time.Since(start); got != 30*time.Millisecond {
			t.Fatalf("Think returned after %v, want 30ms (when the context was cancelled)", got)
		}
		if u.think != 30*time.Millisecond {
			t.Errorf("think = %v, want the 30ms that were waited", u.think)
		}
	})
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
	if len(iters) != 1 || iters[0].Err != record.ErrPanic {
		t.Fatalf("iters = %+v", iters)
	}
}

// A step that panics after sending a request is recorded as failed (kind panic), the panic value and
// stack are in the iteration's error, and the request recorded before the panic is still there.
// writeToMap panics with a runtime error. The nil map comes in as an argument so that static analysis
// (staticcheck SA5000) does not flag the tests that rely on the panic.
func writeToMap(m map[string]int) { m["x"] = 1 }

func TestPanicInStepIsRecorded(t *testing.T) {
	// A fake clock and an in-memory server: the request takes exactly 7ms and the step takes exactly that long
	// (the panic comes right after it), on every OS. A real clock cannot assert this: Windows' coarse
	// monotonic clock measured a step this short as 0.
	inBubble(t, func(t *testing.T) {
		const latency = 7 * time.Millisecond
		col := record.NewCollector()
		u := testUser(t, "http://stub.test", 4, 2, col)
		u.Client.Transport = fakeServerAfter(latency)
		sc := NewScenario("checkout", func(ctx context.Context, u *User) error {
			return u.Step("a", func(s *Step) error {
				s.Get("/").Do()
				writeToMap(nil) // panics
				return nil
			})
		})
		begin := time.Now()
		err := sc.run(u)
		var pe *panicError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v, want a *panicError", err)
		}
		if _, ok := pe.value.(runtime.Error); !ok {
			t.Errorf("panic value = %#v, want the runtime error", pe.value)
		}
		if !strings.Contains(string(pe.stack), "TestPanicInStepIsRecorded.func") {
			t.Errorf("stack does not show where the panic happened:\n%s", pe.stack)
		}

		reqs, steps, iters := col.Snapshot()
		if len(reqs) != 1 {
			t.Fatalf("requests = %d, want 1", len(reqs))
		}
		if len(steps) != 1 {
			t.Fatalf("steps = %+v, want one record", steps)
		}
		st := steps[0]
		if st.Step != "a" || st.Err != record.ErrPanic || st.UserID != 4 || st.Iteration != 2 {
			t.Errorf("step = %+v", st)
		}
		if !st.Start.Equal(begin) {
			t.Errorf("step Start = %v, want %v", st.Start, begin)
		}
		if st.Duration != latency {
			t.Errorf("step Duration = %v, want exactly the %v the request took", st.Duration, latency)
		}
		if reqs[0].Latency != latency {
			t.Errorf("request Latency = %v, want %v", reqs[0].Latency, latency)
		}
		if len(iters) != 1 || iters[0].Err != record.ErrPanic {
			t.Errorf("iters = %+v", iters)
		}
	})
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
