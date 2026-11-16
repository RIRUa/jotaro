package jotaro

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Reserve a free port, then give it to the plan.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	sc := NewScenario("m", func(ctx context.Context, u *User) error {
		time.Sleep(5 * time.Millisecond)
		return u.Step("get", func(s *Step) error { return s.Get("/").Do().ExpectStatus(200) })
	})
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), Plan{Scenario: sc, BaseURL: srv.URL, Users: 2, Duration: 600 * time.Millisecond, MetricsAddr: addr})
		done <- err
	}()

	var body string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body = string(b)
		if strings.Contains(body, `jotaro_responses_total{class="2xx"}`) {
			break
		}
	}
	for _, want := range []string{
		"# TYPE jotaro_requests_total counter",
		`jotaro_responses_total{class="2xx"}`,
		`jotaro_request_duration_seconds{quantile="0.95"}`,
		"jotaro_request_duration_seconds_count",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get("http://" + addr + "/metrics"); err == nil {
		t.Error("metrics server still running after Run returned")
	}
}

func TestRunMetricsBadAddr(t *testing.T) {
	sc := NewScenario("m", func(ctx context.Context, u *User) error { return nil })
	_, err := Run(context.Background(), Plan{Scenario: sc, Users: 1, Duration: time.Second, MetricsAddr: "256.0.0.1:bad"})
	if err == nil || !strings.Contains(err.Error(), "MetricsAddr") {
		t.Errorf("err = %v, want an error about MetricsAddr", err)
	}
}
