// Command mix is a sample of the plan-level features of jotaro: a weighted mix of scenarios, waves of users,
// aborting when the server is broken, live progress and metrics, and thresholds per scenario. It starts its own
// mock shop and runs 70% browse and 30% buy against it.
//
//	go run ./examples/mix                          # 12 s, two waves of users
//	go run ./examples/mix -break-after 2s          # the mock fails after 2 s, so AbortOn stops the run
//	go run ./examples/mix -metrics :9090           # also serve http://localhost:9090/metrics while it runs
//	go run ./examples/mix -format json -o result.json
//
// Progress goes to standard error, so that -format json can be piped. The exit status is 1 when a threshold
// is not met or the run was aborted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/signal"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	duration := flag.Duration("duration", 12*time.Second, "run time")
	breakAfter := flag.Duration("break-after", 0, "make the mock server answer 503 after this long; 0 never")
	metrics := flag.String("metrics", "", "address to serve /metrics on while the run is in progress, such as :9090; off if empty")
	check := flag.Bool("check", true, "judge the result against thresholds and exit with 1 if one is not met")
	format := flag.String("format", "text", "result format (text, json, html)")
	out := flag.String("o", "", "where to write the result; standard output if empty")
	rawOut := flag.String("raw", "", "where to write raw data (JSON Lines); not written if empty")
	flag.Parse()

	f, err := report.ParseFormat(*format)
	if err != nil {
		return err
	}
	var checks []report.Threshold
	for _, s := range thresholds {
		th, err := report.ParseThreshold(s)
		if err != nil {
			return err
		}
		checks = append(checks, th)
	}

	mock := &shop{}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	if *breakAfter > 0 {
		defer time.AfterFunc(*breakAfter, func() { mock.broken.Store(true) }).Stop()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	plan := newPlan(srv.URL, *duration)
	plan.Progress = os.Stderr // one line per second: "5s/12s rps=480 p95=38ms err=0%"
	plan.MetricsAddr = *metrics
	if *rawOut != "" {
		file, err := os.Create(*rawOut)
		if err != nil {
			return err
		}
		defer file.Close()
		plan.Raw = file
	}

	// Run can return a report and an error together (AbortOn, a failed write of Raw), so print the report first.
	rep, runErr := jotaro.Run(ctx, plan)
	if rep == nil {
		return runErr // the Plan was invalid or the metrics address could not be used: nothing ran
	}
	if err := writeReport(rep, f, *out); err != nil {
		return err
	}
	if errors.Is(runErr, jotaro.ErrAborted) {
		return fmt.Errorf("stopped early; the report covers the run up to the abort: %w", runErr)
	}
	if runErr != nil {
		return runErr
	}

	if *check {
		results := rep.Check(checks...)
		fmt.Fprintln(os.Stderr)
		results.Print(os.Stderr)
		if !results.Passed() {
			return errors.New("thresholds not met")
		}
	}
	return nil
}

// newPlan is the whole load shape: 70% browse and 30% buy, and two waves of users that go from 5 up to 20,
// hold, and come back down to 5.
func newPlan(baseURL string, d time.Duration) jotaro.Plan {
	wave := d / 6
	return jotaro.Plan{
		BaseURL: baseURL,
		Scenarios: []jotaro.Weighted{
			{Scenario: browse, Weight: 70},
			{Scenario: buy, Weight: 30},
		},
		StartUsers: 5, // already running at the start, so the first wave does not begin from 0
		UserStages: jotaro.RepeatUserStages(2,
			jotaro.UserStage{Target: 20, For: wave},
			jotaro.UserStage{Target: 20, For: wave},
			jotaro.UserStage{Target: 5, For: wave},
		),
		Duration: d,
		Timeout:  5 * time.Second,
		Grace:    5 * time.Second,
		// Stop hitting a broken server. The conditions describe the state to stop at, and For is how long it must hold.
		AbortOn: []jotaro.AbortRule{
			{When: "http_5xx_rate>50%", For: 2 * time.Second},
			{When: "error_rate>50%", For: 2 * time.Second},
		},
	}
}

// writeReport writes rep to the file at path, or to standard output if path is empty.
func writeReport(rep *report.Report, f report.Format, path string) error {
	w := io.Writer(os.Stdout)
	if path != "" {
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		defer file.Close()
		w = file
	}
	return rep.Write(w, f)
}
