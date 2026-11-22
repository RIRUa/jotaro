package jotaro

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// stubTransport answers every request with a canned 200 and a small JSON body, without any network.
// It isolates the cost that belongs to the generator (building the request, tracing, recording)
// from the cost of net/http's transport and of the target.
type stubTransport struct {
	body string
}

func (s stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(s.body)),
		ContentLength: int64(len(s.body)),
		Request:       req,
	}, nil
}

// benchRunner is the part of Run that one iteration touches: a streaming collector that folds records
// into the report aggregator (as Run does without Raw and Progress), and the pieces newUser needs.
type benchRunner struct {
	p   Plan
	tr  http.RoundTripper
	col *record.Collector
}

func newBenchRunner(tr http.RoundTripper, sc *Scenario) *benchRunner {
	return &benchRunner{
		p:   Plan{Scenario: sc, BaseURL: "http://stub.invalid"},
		tr:  tr,
		col: record.NewStreamingCollector(report.NewAggregator(time.Now())),
	}
}

func singleGetScenario() *Scenario {
	return NewScenario("get", func(_ context.Context, u *User) error {
		return u.Step("get", func(s *Step) error {
			return s.Get("/items").Query("page", "1").Bearer("token").Do().ExpectStatus(200)
		})
	})
}

func postJSONScenario() *Scenario {
	payload := map[string]any{"name": "alice", "qty": 3, "tags": []string{"a", "b"}}
	return NewScenario("post", func(_ context.Context, u *User) error {
		return u.Step("post", func(s *Step) error {
			return s.Post("/orders").Bearer("token").JSON(payload).Do().ExpectStatus(200)
		})
	})
}

func fiveStepsScenario() *Scenario {
	return NewScenario("five", func(_ context.Context, u *User) error {
		for _, name := range []string{"a", "b", "c", "d", "e"} {
			err := u.Step(name, func(s *Step) error {
				return s.Get("/"+name).Query("page", "1").Bearer("token").Do().ExpectStatus(200)
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func benchIterations(b *testing.B, sc *Scenario, body string, open bool) {
	br := newBenchRunner(stubTransport{body: body}, sc)
	ctx := context.Background()
	at := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u, err := newUser(ctx, br.p, sc, i, 0, br.tr, 30*time.Second, br.col)
		if err != nil {
			b.Fatal(err)
		}
		if open {
			u.scheduled.set(at)
		}
		if err := sc.run(u); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIteration is one iteration of one GET with a query parameter and a bearer header: the
// number the issue quotes. The stub transport does no I/O, so it is the generator's own cost.
func BenchmarkIteration(b *testing.B) {
	benchIterations(b, singleGetScenario(), `{"ok":true}`, false)
}

// BenchmarkIterationPostJSON is one POST with a JSON body per iteration.
func BenchmarkIterationPostJSON(b *testing.B) {
	benchIterations(b, postJSONScenario(), `{"id":1}`, false)
}

// BenchmarkIterationFiveSteps is five steps of one request each per iteration, so the cost that is
// paid once per iteration (the user) is told apart from the cost paid per request.
func BenchmarkIterationFiveSteps(b *testing.B) {
	benchIterations(b, fiveStepsScenario(), `{"ok":true}`, false)
}

// BenchmarkIterationOpenModel is BenchmarkIteration with a scheduled arrival time set, as the open
// model does for every arrival.
func BenchmarkIterationOpenModel(b *testing.B) {
	benchIterations(b, singleGetScenario(), `{"ok":true}`, true)
}

// BenchmarkRunEndToEnd runs a whole closed-model Run against an in-process httptest server and reports
// requests per second per core (GOMAXPROCS). The server shares the machine with the generator, so
// this is a lower bound of what one generator core can drive, not a property of a remote target.
func BenchmarkRunEndToEnd(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	const users = 16
	plan := Plan{
		Scenario: singleGetScenario(),
		BaseURL:  srv.URL,
		Users:    users,
		Duration: time.Second,
	}
	b.ReportAllocs()
	var requests, runs int
	var elapsed time.Duration
	for b.Loop() {
		rep, err := Run(context.Background(), plan)
		if err != nil {
			b.Fatal(err)
		}
		requests += rep.Requests.Count
		elapsed += rep.Elapsed
		runs++
	}
	if elapsed > 0 {
		rps := float64(requests) / elapsed.Seconds()
		b.ReportMetric(rps, "req/s")
		b.ReportMetric(rps/float64(runtime.GOMAXPROCS(0)), "req/s/core")
	}
}

// BenchmarkNetHTTPFloor is the same request sent with net/http directly (request, client, ReadAll) against
// the stub transport, with none of jotaro's code. What BenchmarkIteration spends above this is the generator's
// own share; this much belongs to net/http and the stub itself.
func BenchmarkNetHTTPFloor(b *testing.B) {
	tr := stubTransport{body: `{"ok":true}`}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://stub.invalid/items?page=1", nil)
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer token")
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}
