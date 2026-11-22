package jotaro

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// cookieServer sets a session cookie on /login and reports on /who whether the request carried it.
func cookieServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/"})
		case "/who":
			if _, err := r.Cookie("sid"); err == nil {
				w.Header().Set("X-Cookie", "yes")
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNewUserCookiesStayInTheIteration pins that the lazily created jar is per user: a cookie set in one
// iteration is sent later in that iteration and never leaks into the next one.
func TestNewUserCookiesStayInTheIteration(t *testing.T) {
	srv := cookieServer(t)
	col := record.NewCollector()
	p := Plan{BaseURL: srv.URL}
	sc := NewScenario("s", func(_ context.Context, u *User) error {
		return nil
	})
	who := func(u *User) string {
		var got string
		err := u.Step("who", func(s *Step) error {
			resp := s.Get("/who").Do()
			got = resp.Header.Get("X-Cookie")
			return resp.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	login := func(u *User) {
		if err := u.Step("login", func(s *Step) error { return s.Get("/login").Do().Err() }); err != nil {
			t.Fatal(err)
		}
	}

	u1 := newUser(context.Background(), p, sc, 1, 0, http.DefaultTransport, time.Second, col)
	if got := who(u1); got != "" {
		t.Fatalf("a fresh user sent a cookie: %q", got)
	}
	login(u1)
	if got := who(u1); got != "yes" {
		t.Fatalf("the cookie was not kept within the iteration: %q", got)
	}

	u2 := newUser(context.Background(), p, sc, 1, 1, http.DefaultTransport, time.Second, col)
	if got := who(u2); got != "" {
		t.Fatalf("the next iteration inherited the cookie: %q", got)
	}
	if u1.Client.Jar == u2.Client.Jar {
		t.Fatal("two iterations share a cookie jar")
	}
}

// TestLazyJarConcurrentFirstCookies has parallel requests of one step receive their first cookies at the same
// time; run with -race. All of the cookies must end up in one jar.
func TestLazyJarConcurrentFirstCookies(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "c" + string(rune('a'+n.Add(1))), Value: "1", Path: "/"})
	}))
	defer srv.Close()

	col := record.NewCollector()
	sc := NewScenario("s", func(_ context.Context, u *User) error { return nil })
	u := newUser(context.Background(), Plan{BaseURL: srv.URL}, sc, 0, 0, http.DefaultTransport, time.Second, col)
	const k = 8
	fns := make([]func() error, k)
	err := u.Step("p", func(s *Step) error {
		for i := range fns {
			fns[i] = func() error { return s.Get("/").Do().Err() }
		}
		return s.Parallel(fns...)
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if got := len(u.Client.Jar.Cookies(req.URL)); got != k {
		t.Fatalf("jar holds %d cookies, want %d", got, k)
	}
}

// TestRequestsDoNotMutateEachOthersHeaders pins that the header map of a built request is not shared with the
// request that goes out: the cookie jar adds a Cookie header to the outgoing request only.
func TestRequestsDoNotMutateEachOthersHeaders(t *testing.T) {
	srv := cookieServer(t)
	col := record.NewCollector()
	sc := NewScenario("s", func(_ context.Context, u *User) error { return nil })
	u := newUser(context.Background(), Plan{BaseURL: srv.URL}, sc, 0, 0, http.DefaultTransport, time.Second, col)
	err := u.Step("h", func(s *Step) error {
		if err := s.Get("/login").Do().Err(); err != nil {
			return err
		}
		r := s.Get("/who").Bearer("t")
		if err := r.Do().Err(); err != nil {
			return err
		}
		if got := r.Spec.Header.Get("Cookie"); got != "" {
			t.Errorf("the builder header was mutated: Cookie = %q", got)
		}
		if got := r.Spec.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("Authorization = %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestTraceFieldsArePopulated pins that the connection phases still reach the record: the first request of a
// fresh transport opens a connection (a new Conn, then a reused one) and a time to first byte never exceeds the latency.
func TestTraceFieldsArePopulated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	col := record.NewCollector()
	sc := NewScenario("s", func(_ context.Context, u *User) error { return nil })
	u := newUser(context.Background(), Plan{BaseURL: srv.URL}, sc, 0, 0, tr, time.Second, col)
	for range 2 {
		if err := u.Step("t", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) }); err != nil {
			t.Fatal(err)
		}
	}
	reqs, _, _ := col.Snapshot()
	if len(reqs) != 2 {
		t.Fatalf("records = %d, want 2", len(reqs))
	}
	// Durations are only compared loosely: a coarse clock (Windows) can measure a loopback connect as 0.
	if reqs[0].Conn != record.ConnNew {
		t.Errorf("first request: Conn = %q, want %q", reqs[0].Conn, record.ConnNew)
	}
	if reqs[1].Conn != record.ConnReused || reqs[1].Connect != 0 {
		t.Errorf("second request: Conn = %q, Connect = %v; want a reused connection", reqs[1].Conn, reqs[1].Connect)
	}
	for i, r := range reqs {
		if r.TTFB < 0 || r.TTFB > r.Latency {
			t.Errorf("request %d: TTFB = %v, Latency = %v", i, r.TTFB, r.Latency)
		}
	}
}

// TestReadBody pins that the pre-sized read returns what io.ReadAll would, whatever the declared length.
func TestReadBody(t *testing.T) {
	body := strings.Repeat("x", 1000)
	cases := []struct {
		name string
		r    io.Reader
		size int64
		want string
	}{
		{"exact", strings.NewReader(body), 1000, body},
		{"declared too small", strings.NewReader(body), 10, body},
		{"declared too large", strings.NewReader(body), 5000, body},
		{"unknown", strings.NewReader(body), -1, body},
		{"over the cap", strings.NewReader(body), maxPresize + 1, body},
		{"empty with a declaration", strings.NewReader(""), 12, ""},
		{"no body", http.NoBody, 12, ""},
		{"one byte reads", iotest.OneByteReader(strings.NewReader(body)), 1000, body},
		{"zero", strings.NewReader(""), 0, ""},
	}
	for _, c := range cases {
		got, err := readBody(c.r, c.size)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if got == nil || string(got) != c.want {
			t.Errorf("%s: got %q (nil: %v), want %q", c.name, got, got == nil, c.want)
		}
	}

	boom := errors.New("boom")
	got, err := readBody(io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(boom)), 10)
	if !errors.Is(err, boom) || string(got) != "ab" {
		t.Errorf("read error: got %q, %v", got, err)
	}
}

// TestEmptyBodyRequests pins what goes out for a request without a body, with and without a method that
// usually has one.
func TestEmptyBodyRequests(t *testing.T) {
	type seen struct {
		method string
		cl     int64
		body   string
	}
	var (
		mu  sync.Mutex
		got []seen
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.ContentLength, string(b)})
		mu.Unlock()
	}))
	defer srv.Close()
	col := record.NewCollector()
	sc := NewScenario("s", func(_ context.Context, u *User) error { return nil })
	u := newUser(context.Background(), Plan{BaseURL: srv.URL}, sc, 0, 0, http.DefaultTransport, time.Second, col)
	err := u.Step("e", func(s *Step) error {
		for _, r := range []*Request{s.Get("/"), s.Post("/"), s.Post("/").Body("hi")} {
			if err := r.Do().Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []seen{{"GET", 0, ""}, {"POST", 0, ""}, {"POST", 2, "hi"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d: got %v, want %v", i, got[i], want[i])
		}
	}
}
