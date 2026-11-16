package jotaro

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/RIRUa/jotaro/report"
)

// metricsHandler serves the state of a run in the Prometheus text exposition format (version 0.0.4).
// It is written by hand so no dependency is needed. snapshot returns the report of the run so far.
func metricsHandler(snapshot func() *report.Report) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, snapshot())
	})
}

// writeMetrics writes rep as Prometheus metrics. Errors from w are ignored: a broken scrape must not fail the run.
func writeMetrics(w io.Writer, rep *report.Report) {
	gauge := func(name, help string, v float64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
	}
	counter := func(name, help string, v float64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %g\n", name, help, name, name, v)
	}
	counter("jotaro_requests_total", "Requests finished so far.", float64(rep.Requests.Count))
	counter("jotaro_dropped_total", "Arrivals not run because of the concurrency cap.", float64(rep.Dropped))
	counter("jotaro_bytes_received_total", "Response body bytes received.", float64(rep.Requests.BytesIn))
	counter("jotaro_bytes_sent_total", "Request body bytes sent.", float64(rep.Requests.BytesOut))
	gauge("jotaro_elapsed_seconds", "Seconds since the run started.", rep.Elapsed.Seconds())
	gauge("jotaro_requests_per_second", "Average requests per second over the run so far.", rep.RPS())

	fmt.Fprint(w, "# HELP jotaro_responses_total Responses by status class.\n# TYPE jotaro_responses_total counter\n")
	for _, k := range sortedKeys(rep.Requests.Status) {
		fmt.Fprintf(w, "jotaro_responses_total{class=%q} %d\n", k, rep.Requests.Status[k])
	}
	fmt.Fprint(w, "# HELP jotaro_errors_total Network errors by kind.\n# TYPE jotaro_errors_total counter\n")
	errs := map[string]int{}
	for k, n := range rep.Requests.Errors {
		errs[string(k)] = n
	}
	for _, k := range sortedKeys(errs) {
		fmt.Fprintf(w, "jotaro_errors_total{kind=%q} %d\n", k, errs[k])
	}

	l := rep.Requests.Latency
	fmt.Fprint(w, "# HELP jotaro_request_duration_seconds Request latency over the run so far.\n# TYPE jotaro_request_duration_seconds summary\n")
	for _, q := range []struct {
		label string
		d     time.Duration
	}{{"0.5", l.P50}, {"0.9", l.P90}, {"0.95", l.P95}, {"0.99", l.P99}} {
		fmt.Fprintf(w, "jotaro_request_duration_seconds{quantile=%q} %g\n", q.label, q.d.Seconds())
	}
	fmt.Fprintf(w, "jotaro_request_duration_seconds_sum %g\njotaro_request_duration_seconds_count %d\n",
		l.Mean.Seconds()*float64(l.Count), l.Count)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// startMetricsServer listens on addr and serves /metrics until the returned stop function is called.
// The listener is opened before it returns, so a bad address fails the run at once.
func startMetricsServer(addr string, snapshot func() *report.Report) (stop func(), err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("jotaro: Plan.MetricsAddr: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler(snapshot))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}, nil
}
