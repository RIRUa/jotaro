# Writing a jotaro scenario

jotaro load-tests HTTP servers with scenarios written as plain Go code.
Module: `github.com/RIRUa/jotaro` (Go 1.25+). Packages: `jotaro` (scenarios, load), `report` (output, thresholds), `record` (raw data).

## Skeleton

A runnable load test is one `package main` file: a `Scenario`, a `Plan`, `jotaro.Run`, a saved result and a report.

```go
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
	var login struct {
		Token string `json:"token"`
	}
	if err := u.Step("login", func(s *jotaro.Step) error {
		return s.Post("/login").JSON(map[string]string{"user": "alice"}).Do().Decode(&login)
	}); err != nil {
		return err // return err to stop this iteration; ignore it to continue
	}

	u.Think(50 * time.Millisecond) // waiting time, not counted in latency

	return u.Step("order", func(s *jotaro.Step) error {
		return s.Post("/orders").Bearer(login.Token).
			JSON(map[string]int{"item": 1}).
			Do().ExpectStatus(http.StatusCreated)
	})
})

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt) // Ctrl-C stops the run and keeps the result so far
	defer stop()

	rep, err := jotaro.Run(ctx, jotaro.Plan{
		Scenario: checkout,
		BaseURL:  "http://localhost:8080",
		Duration: time.Minute,
		Users:    20,
		// Stop early when the server is clearly broken, instead of hitting it for the whole Duration.
		AbortOn: []jotaro.AbortRule{{When: "error_rate>50%", For: 10 * time.Second}},
	})
	// When AbortOn stops the run, Run returns the report up to that point together with an error that
	// wraps jotaro.ErrAborted. That is not a failure to run: save the result first, then report the abort.
	aborted := errors.Is(err, jotaro.ErrAborted)
	if err != nil && !aborted {
		log.Fatal(err) // for example an invalid Plan: nothing ran, so there is no result
	}

	// Save the result so that it can be read later (summarize_result, check_result, jotaro check).
	if err := os.MkdirAll("results", 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create("results/result.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := rep.Write(f, report.FormatJSON); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}

	if err := rep.Write(os.Stdout, report.FormatText); err != nil {
		log.Fatal(err)
	}
	if aborted {
		log.Fatal(err) // exit code 1, after the result is saved
	}
}
```

Run it from the directory of the file with `go run .` (a module that requires jotaro; `go get github.com/RIRUa/jotaro` adds it). It prints the text report and writes `results/result.json` relative to the directory it is run from, so pass the absolute path of that file to `summarize_result` and `check_result`.

## Concepts

- `jotaro.NewScenario(name, fn)`: the flow one virtual user runs once. `fn` has the signature `func(ctx context.Context, u *jotaro.User) error`.
- `u.Step(name, fn)`: the unit of measurement. Results are aggregated per step name. `fn` has the signature `func(s *jotaro.Step) error` and its error is returned as is, so the caller decides whether to continue or stop.
- Even when a step fails, the iteration counts as successful as long as the scenario function returns `nil`. Step failures show up in the per-step stats. To have a failed step (for example a failed `Expect*` check) count as a failed iteration, return its error from the scenario function; `iteration_fail_rate` then reflects it.
- `u.Think(d)`: think time. Counted neither in a step nor in active time.
- Cookies are fresh for each iteration. The connection pool is shared by all users.
- `u.ID` / `u.Iteration` identify the virtual user and the iteration (0-based). `u.Client` is the raw `*http.Client`; requests sent through it are **not recorded**.

## Building requests

Inside a step, build with `s.Get` / `s.Post` / `s.Put` / `s.Patch` / `s.Delete` / `s.Request(method, path)` and send with `Do()`.
Paths are relative to `Plan.BaseURL`.

| Chain before `Do()` | Meaning |
|---|---|
| `Header(k, v)` / `Headers(map)` | set headers |
| `Bearer(token)` / `Basic(user, pass)` | Authorization header |
| `Query(k, v)` / `Queries(url.Values)` | query parameters (escaped; prefer these over concatenating into the path) |
| `JSON(v)` | JSON body |
| `Form(url.Values)` | `application/x-www-form-urlencoded` body |
| `Body(v)` | `[]byte` / `string` / `io.Reader` sent as is (no Content-Type), `url.Values` as form, anything else as JSON |

`Do()` returns a `*Response` (fields `Code`, `Header`, `Body`). Finish the chain with one of:

- `.Decode(&v)`: read the body as JSON (returns the network error if any)
- `.ExpectStatus(code)`: verify the status code
- `.ExpectStatusIn(codes ...int)`: the status code is one of `codes`
- `.ExpectHeader(key, want)`: the response header `key` equals `want`
- `.ExpectBodyContains(substr)`: the body contains `substr`
- `.ExpectJSON(path, want)`: the value at `path` in the JSON body equals `want` (compared in JSON form, so `ExpectJSON("n", 3)` matches `{"n": 3}`)
- `.Err()`: the network error only (HTTP 4xx/5xx are **not** network errors)

Every `Expect*` returns a `*jotaro.ExpectError` on mismatch (the step fails with `expect_mismatch`), and the network error instead if the request itself failed. Each returns one `error`, so to check several things keep the response: `resp := s.Get("/x").Do(); if err := resp.ExpectStatus(200); err != nil { return err }; return resp.ExpectJSON("data.ok", true)`.

`resp.JSON(path)` returns `(any, error)`: the value at a dot-separated `path` of the JSON body (`"data.items.0.id"`: object keys by name, array elements by index, `""` for the whole body; numbers are `float64`). Use it to pull one value out without declaring a struct, for example a token for the next step.

**Always finish with `Decode`, an `Expect*` check, or an explicit check of `Err()`/`Code`.** A bare `s.Get("/x").Do()` records the request but never marks a wrong status as a failure.

## Data per user: Feeder

```go
var accounts = jotaro.NewFeeder([]map[string]string{
	{"user": "alice"}, {"user": "bob"},
}, jotaro.Circular) // Circular | Random | Once

creds, ok := accounts.Next() // ok is false when empty or when Once has run out
```

`jotaro.LoadCSV(r io.Reader)` reads a CSV (first line is the header) into `[]map[string]string`.

## Choosing the load: Plan

Specify **exactly one** of `Users`, `UserStages` and `Arrival`. `Duration` is required.

| Setting | Meaning |
|---|---|
| `Users: 20` | closed model: 20 users repeat the scenario for `Duration` |
| `UserStages: []jotaro.UserStage{{Target: 100, For: 30*time.Second}, {Target: 100, For: time.Minute}, {Target: 0, For: 10*time.Second}}` | closed model with a changing number of users: ramp up, hold, ramp down (starts from `StartUsers`, 0 by default). A user stopped by a lower target finishes its current iteration first |
| `UserStages: jotaro.RepeatStages(3, jotaro.UserStage{Target: 100, For: 20*time.Second}, jotaro.UserStage{Target: 10, For: 20*time.Second})` | repeat a sequence of stages (waves of load); join with other stages using `append` |
| `Arrival: jotaro.Rate(50, time.Second)` | open model: start 50 scenarios per second |
| `Arrival: jotaro.Ramp(0, 100, time.Second, 10*time.Second)` | rate goes from 0 to 100 per second over 10 seconds |
| `Arrival: jotaro.Stages(jotaro.Stage{Arrival: ..., For: ...}, ...)` | several stages in order (ramp up → hold → ramp down) |

To run several flows in one plan, set `Scenarios` instead of `Scenario` (exactly one of the two): `Scenarios: []jotaro.Weighted{{Scenario: browse, Weight: 7}, {Scenario: checkout, Weight: 3}}`. Each iteration (closed model) or arrival (open model) picks one scenario at random in proportion to its `Weight` (here 70% / 30%; only the ratio matters). The scenarios need distinct names, because the report and thresholds separate results by scenario name (`p95{scenario=checkout}<300ms`).

In the open model arrivals keep their pace even when the server slows down, and latency is measured from the scheduled start time (Coordinated Omission correction). Use it to find out how the server behaves under a given request rate; use the closed model for a fixed number of concurrent users.

Other `Plan` fields: `MaxUsers` (concurrency cap in the open model, default 1000; arrivals beyond it are counted as dropped), `Timeout` (per request, default 30s), `Grace` (wait for running iterations after `Duration`, default 30s), `Raw` (an `io.Writer` for raw JSON Lines data), `Progress` (an `io.Writer` such as `os.Stderr` that gets one progress line per second during the run), `MetricsAddr` (such as `":9090"`; serves live Prometheus metrics at `/metrics` during the run), `AbortOn` (`[]jotaro.AbortRule{{When: "http_5xx_rate>50%", For: 5 * time.Second}}` stops the run early once the condition has held for `For`; `Run` then returns the partial report and an error that satisfies `errors.Is(err, jotaro.ErrAborted)`, so do not treat that error as a failure to build the plan).

`Stages` ends when the stages end; set `Duration` to the sum of every stage's `For`.

## Output and thresholds

```go
rep.Write(os.Stdout, report.FormatText) // FormatText | FormatJSON | FormatHTML

th, err := report.ParseThreshold("p95<200ms")
checks := rep.Check(th)
checks.Print(os.Stdout)
if !checks.Passed() {
	os.Exit(1)
}
```

See the `jotaro://thresholds` resource for the threshold syntax, and the `jotaro://result` resource for how to save a result and what its numbers mean. A saved JSON result can be read later with the `summarize_result` tool and judged with the `check_result` tool or `jotaro check -t 'p95<200ms' result.json`.

## Checklist

1. Every `u.Step` has a distinct, stable name (it is the key in the report and in thresholds).
2. Every request ends with `Decode`, an `Expect*` check (`ExpectStatus`, `ExpectStatusIn`, `ExpectHeader`, `ExpectBodyContains`, `ExpectJSON`), or an explicit check.
3. Data extracted from one step (tokens, IDs) is declared outside the step closure and checked before the next step uses it.
4. `Plan` sets exactly one of `Users` / `UserStages` / `Arrival`, and a positive `Duration`.
5. Only HTTP can be measured.
6. Do not point the scenario at a server you do not own or are not allowed to load-test.

## Where the files go

For a small service, keep the load test in its own `package main` directory of the service's module:

```
myservice/
├─ go.mod
└─ loadtest/
   ├─ main.go          # flags, Plan, Run, save the result, Check
   ├─ checkout.go      # scenario (package main)
   ├─ thresholds.go    # var thresholds = []string{"p95<200ms", ...}
   ├─ testdata/        # CSV for Feeder; no secrets
   └─ results/         # git-ignored: result.json, report.html, raw.jsonl
```

Run it inside `loadtest/` with `go run .`: the paths in the code (`results/result.json`, `testdata/...`) are relative to the directory it is run from. Add `loadtest/results/` to `.gitignore`. `check_scenario` checks one file at a time and cannot see the other files of the package, so write a single self-contained file to check it first, and split it into the layout above afterwards.
