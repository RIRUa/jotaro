package jotaro

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// connServer is a test server that knows how many distinct connections carried a request.
// It counts the client address of each handled request: that is exactly the number of connections that
// were used at least once, whatever the protocol (HTTP/1.1, HTTP/2, plain or TLS).
type connServer struct {
	*httptest.Server

	mu      sync.Mutex
	clients map[string]struct{}
	// accepted is the number of http.StateNew transitions, the number the issue measured.
	accepted int
}

func newConnServer(t *testing.T, mode string) *connServer {
	t.Helper()
	cs := &connServer{clients: map[string]struct{}{}}
	cs.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.clients[r.RemoteAddr] = struct{}{}
		cs.mu.Unlock()
		// Keep requests overlapping so that the users need several connections at once.
		time.Sleep(5 * time.Millisecond)
		w.Write([]byte("ok"))
	}))
	cs.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			cs.mu.Lock()
			cs.accepted++
			cs.mu.Unlock()
		}
	}
	switch mode {
	case "http":
		cs.Start()
	case "https":
		cs.StartTLS()
	case "h2":
		cs.EnableHTTP2 = true
		cs.StartTLS()
	}
	t.Cleanup(cs.Close)
	if mode != "http" {
		// Run clones http.DefaultTransport, so this is how a test certificate is trusted.
		dt, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			t.Skip("http.DefaultTransport is not an *http.Transport")
		}
		old := dt.TLSClientConfig
		dt.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
		t.Cleanup(func() { dt.TLSClientConfig = old })
	}
	return cs
}

func (cs *connServer) used() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.clients)
}

func (cs *connServer) acceptedConns() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.accepted
}

func runConnLoad(t *testing.T, cs *connServer, users int) *report.Report {
	t.Helper()
	sc := NewScenario("conn", func(ctx context.Context, u *User) error {
		return u.Step("get", func(s *Step) error {
			return s.Get("/").Do().ExpectStatus(200)
		})
	})
	rep, err := Run(context.Background(), Plan{
		Scenario: sc, BaseURL: cs.URL, Users: users, Duration: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestRunCountsOpenedConnections checks that NewConns is the number of connections that served a request,
// as the server sees it. Before the fix a connection that was dialed for one request but parked in the idle
// pool and used by another was reported as reused, so over TLS (slow dials) the count came out low.
func TestRunCountsOpenedConnections(t *testing.T) {
	for _, mode := range []string{"http", "https", "h2"} {
		t.Run(mode, func(t *testing.T) {
			cs := newConnServer(t, mode)
			rep := runConnLoad(t, cs, 20)

			q := rep.Requests
			if q.Count == 0 || len(q.Errors) > 0 {
				t.Fatalf("unexpected run: count=%d errors=%v", q.Count, q.Errors)
			}
			if got, want := q.NewConns, cs.used(); got != want {
				t.Errorf("NewConns = %d, server saw %d connections carry requests", got, want)
			}
			if got := q.NewConns + q.ReusedConns; got != q.Count {
				t.Errorf("NewConns+ReusedConns = %d, want every request counted once (%d)", got, q.Count)
			}
			if q.NewConns > cs.acceptedConns() {
				t.Errorf("NewConns = %d exceeds the %d connections the server accepted", q.NewConns, cs.acceptedConns())
			}
			if mode == "h2" && q.NewConns > 20 {
				t.Errorf("HTTP/2 NewConns = %d, want at most one connection per user", q.NewConns)
			}
			var perSec, perStep int
			for _, p := range rep.Series {
				perSec += p.NewConns
			}
			for _, st := range rep.Scenarios[0].Steps {
				perStep += st.Requests.NewConns
			}
			if perSec != q.NewConns || perStep != q.NewConns {
				t.Errorf("new conns: series %d, steps %d, total %d; want all equal", perSec, perStep, q.NewConns)
			}
		})
	}
}

// TestRunKeepAliveReuse checks that a single user keeps one connection and that all later requests
// are counted as reused.
func TestRunKeepAliveReuse(t *testing.T) {
	for _, mode := range []string{"http", "https"} {
		t.Run(mode, func(t *testing.T) {
			cs := newConnServer(t, mode)
			rep := runConnLoad(t, cs, 1)

			q := rep.Requests
			if q.Count < 2 {
				t.Fatalf("Count = %d, want at least 2 requests", q.Count)
			}
			if q.NewConns != 1 || q.ReusedConns != q.Count-1 {
				t.Errorf("new=%d reused=%d of %d requests, want 1 new and the rest reused", q.NewConns, q.ReusedConns, q.Count)
			}
			if cs.used() != 1 {
				t.Errorf("server saw %d connections, want 1", cs.used())
			}
		})
	}
}

// TestConnFirstUse covers the cases the load tests cannot reach on demand.
func TestConnFirstUse(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	tc := &trackedConn{Conn: a}

	// The first request to get the connection opened it, whether or not net/http calls it reused.
	if !connFirstUse(httptrace.GotConnInfo{Conn: tc, Reused: true, WasIdle: true}) {
		t.Error("first use of a tracked connection reported as reused")
	}
	if connFirstUse(httptrace.GotConnInfo{Conn: tc, Reused: false}) {
		t.Error("second use of a tracked connection reported as new")
	}

	// TLS (and anything else with NetConn) is unwrapped down to the tracked connection.
	tc2 := &trackedConn{Conn: a}
	wrapped := tls.Client(tc2, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // never handshakes
	if !connFirstUse(httptrace.GotConnInfo{Conn: wrapped}) || connFirstUse(httptrace.GotConnInfo{Conn: wrapped}) {
		t.Error("a TLS connection over a tracked one is not tracked")
	}

	// Without a tracked connection the flag of net/http decides.
	if !connFirstUse(httptrace.GotConnInfo{Conn: b, Reused: false}) {
		t.Error("untracked connection with Reused=false should be new")
	}
	if connFirstUse(httptrace.GotConnInfo{Conn: b, Reused: true}) {
		t.Error("untracked connection with Reused=true should be reused")
	}
	if !connFirstUse(httptrace.GotConnInfo{}) {
		t.Error("nil connection with Reused=false should be new")
	}
}

// TestTracerKeepsNewConn checks that a request which got a connection of its own and then another
// (a retry) is still counted as the one that opened a connection.
func TestTracerKeepsNewConn(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	fresh, used := &trackedConn{Conn: a}, &trackedConn{Conn: b}
	used.firstUse()

	tr := &tracer{}
	got := tr.clientTrace().GotConn
	got(httptrace.GotConnInfo{Conn: fresh})
	got(httptrace.GotConnInfo{Conn: used, Reused: true})
	var rec record.Record
	tr.fill(&rec)
	if rec.Conn != record.ConnNew {
		t.Errorf("Conn = %q, want %q", rec.Conn, record.ConnNew)
	}
}

// TestTrackDials checks that the dial wrappers keep the dial they wrap and hand out tracked connections.
func TestTrackDials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	var viaContext, viaLegacy int
	cases := map[string]*http.Transport{
		"default": {},
		"DialContext": {DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
			viaContext++
			return (&net.Dialer{}).DialContext(ctx, n, a)
		}},
		"Dial": {Dial: func(n, a string) (net.Conn, error) {
			viaLegacy++
			return net.Dial(n, a)
		}},
	}
	for name, tr := range cases {
		trackDials(tr)
		c, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := c.(*trackedConn); !ok {
			t.Errorf("%s: dialed %T, want *trackedConn", name, c)
		}
		c.Close()
	}
	if viaContext != 1 || viaLegacy != 1 {
		t.Errorf("wrapped dials called %d / %d times, want 1 / 1", viaContext, viaLegacy)
	}
	tr := &http.Transport{}
	trackDials(tr)
	if _, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil {
		t.Error("dial to a closed port should fail")
	}
}

// BenchmarkConnFirstUse measures what the connection tracking adds to each request (one GotConn call).
func BenchmarkConnFirstUse(b *testing.B) {
	a, c := net.Pipe()
	defer a.Close()
	defer c.Close()
	tc := tls.Client(&trackedConn{Conn: a}, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // never handshakes
	info := httptrace.GotConnInfo{Conn: tc, Reused: true}
	b.ReportAllocs()
	for b.Loop() {
		connFirstUse(info)
	}
}
