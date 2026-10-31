// Command marketplace is a deeper sample than examples/checkout. One scenario plays five kinds of
// visitors against a stateful mock shop that has expiring sessions, CSRF tokens, sold-out items,
// a rate-limited and lossy checkout, and asynchronous payments.
// It shows how to write a scenario that branches, retries, polls and re-logs in, and how to
// turn the result into pass/fail thresholds.
//
//	go run ./examples/marketplace                         # 20 s, open model: ramp up → hold → spike → hold → ramp down
//	go run ./examples/marketplace -rate 40 -duration 30s  # a heavier run
//	go run ./examples/marketplace -users 50               # closed model with 50 fixed visitors
//	go run ./examples/marketplace -pace 0.2               # think times at 20%, for a quick look
//	go run ./examples/marketplace -format html -o result.html
//
// The tally of what visitors did and what the mock server saw is written to standard error,
// so that -format json can be piped. The exit status is 1 when a threshold is not met.
package main

import (
	"context"
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

// thresholds are the service-level goals of the run. They are looser than a real SLO because the
// mock injects failures on purpose: a rate limit, lost responses, and sold-out limited editions.
var thresholds = []string{
	// Reading the catalogue should stay fast even in the spike.
	"p95{step=search}<150ms",
	"p95{step=product}<150ms",
	// Checkout does real work and is rate limited, so it gets more room.
	"p95{step=checkout}<500ms",
	// 4xx are mostly business outcomes (sold out, rejected coupon) and expired sessions, 5xx the lost checkout responses.
	// Network errors and unexpected statuses would show up in error_rate and iteration_fail_rate.
	"http_4xx_rate<10%",
	"http_5xx_rate<3%",
	"error_rate<1%",
	// Visitors who gave up for a reason that is not business as usual.
	"iteration_fail_rate<2%",
	// The generator must keep up, or the numbers above mean nothing.
	"dropped<1",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	rate := flag.Int("rate", 20, "visitors arriving per second at the steady stages (open model)")
	users := flag.Int("users", 0, "fixed number of visitors; if above 0, uses the closed model instead of -rate")
	duration := flag.Duration("duration", 20*time.Second, "run time")
	pace := flag.Float64("pace", 1, "multiplies every think time; 0.2 is five times faster")
	ttl := flag.Duration("session-ttl", 4*time.Second, "average lifetime of a login session on the mock server")
	newcomers := flag.Int("newcomers", 40, "how many new accounts the sign-up visitors can use (each only once)")
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

	accounts, err := loadMembers()
	if err != nil {
		return err
	}
	shop := newMarket(*ttl, accounts)
	srv := httptest.NewServer(shop.handler())
	defer srv.Close()

	cfg := &config{
		members:   jotaro.NewFeeder(accounts, jotaro.Circular),
		newcomers: jotaro.NewFeeder(newcomerAccounts(*newcomers), jotaro.Once),
		pace:      *pace,
		tally:     newTally(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	plan := jotaro.Plan{
		Scenario: newScenario(cfg),
		BaseURL:  srv.URL,
		Duration: *duration,
		// Every visitor is slow on purpose, so the cap is generous; "dropped" tells if it was ever hit.
		MaxUsers: 2000,
		Timeout:  5 * time.Second,
		Grace:    15 * time.Second,
	}
	if *users > 0 {
		plan.Users = *users
	} else {
		plan.Arrival = profile(*rate, *duration)
	}
	if *rawOut != "" {
		file, err := os.Create(*rawOut)
		if err != nil {
			return err
		}
		defer file.Close()
		plan.Raw = file
	}

	rep, err := jotaro.Run(ctx, plan)
	if err != nil {
		return err
	}

	w := io.Writer(os.Stdout)
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer file.Close()
		w = file
	}
	if err := rep.Write(w, f); err != nil {
		return err
	}

	printTally(os.Stderr, cfg.tally, &shop.stats)
	if err := shop.checkInventory(); err != nil {
		return fmt.Errorf("mock server: %w", err)
	}
	fmt.Fprintln(os.Stderr, "\nmock server: inventory is consistent (nothing created or lost, cancels restocked)")

	if *check {
		results := rep.Check(checks...)
		fmt.Fprintln(os.Stderr)
		results.Print(os.Stderr)
		if !results.Passed() {
			return fmt.Errorf("thresholds not met")
		}
	}
	return nil
}

// profile shapes the arrival rate over the run: warm up, hold, spike to twice the rate, hold, cool down.
// The spike is what pushes checkout over its rate limit, so the retry path gets exercised.
// The shares of the run are 20 / 30 / 15 / 20 / 15 percent.
func profile(rate int, d time.Duration) jotaro.Arrival {
	share := func(pct int) time.Duration { return d * time.Duration(pct) / 100 }
	return jotaro.Stages(
		jotaro.Stage{Arrival: jotaro.Ramp(0, rate, time.Second, share(20)), For: share(20)},
		jotaro.Stage{Arrival: jotaro.Rate(rate, time.Second), For: share(30)},
		jotaro.Stage{Arrival: jotaro.Rate(2*rate, time.Second), For: share(15)},
		jotaro.Stage{Arrival: jotaro.Rate(rate, time.Second), For: share(20)},
		jotaro.Stage{Arrival: jotaro.Ramp(rate, 0, time.Second, share(15)), For: share(15)},
	)
}

// printTally writes what the visitors did and what the mock server saw.
func printTally(w io.Writer, t *tally, s *serverStats) {
	fmt.Fprintln(w, "\nwhat the visitors did")
	for _, l := range t.lines() {
		fmt.Fprintln(w, "  "+l)
	}
	fmt.Fprintln(w, "\nwhat the mock server saw")
	for _, kv := range []struct {
		name string
		n    int64
	}{
		{"orders created", s.OrdersCreated.Load()},
		{"checkout answered from idempotency key", s.Replays.Load()},
		{"checkout lost response (order created)", s.LostResponses.Load()},
		{"checkout rate limited (429)", s.RateLimited.Load()},
		{"sold out (409)", s.SoldOut.Load()},
		{"orders cancelled", s.Cancelled.Load()},
		{"sessions expired", s.Expired.Load()},
	} {
		fmt.Fprintf(w, "  %-42s %d\n", kv.name, kv.n)
	}
}
