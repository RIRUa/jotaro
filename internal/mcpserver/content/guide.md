# Writing a jotaro scenario

jotaro load-tests HTTP servers with scenarios written as plain Go code.
Module: `github.com/RIRUa/jotaro` (Go 1.25+). Packages: `jotaro` (scenarios, load), `report` (output, thresholds), `record` (raw data).

## Skeleton

A runnable load test is one `package main` file: a `Scenario`, a `Plan`, `jotaro.Run`, and a report.

```go
package main

import (
	"context"
	"net/http"
	"os"
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
	rep, err := jotaro.Run(context.Background(), jotaro.Plan{
		Scenario: checkout,
		BaseURL:  "http://localhost:8080",
		Duration: time.Minute,
		Users:    20,
	})
	if err != nil {
		panic(err)
	}
	rep.Write(os.Stdout, report.FormatText)
}
```

## Concepts

- `jotaro.NewScenario(name, fn)`: the flow one virtual user runs once. `fn` has the signature `func(ctx context.Context, u *jotaro.User) error`.
- `u.Step(name, fn)`: the unit of measurement. Results are aggregated per step name. `fn` has the signature `func(s *jotaro.Step) error` and its error is returned as is, so the caller decides whether to continue or stop.
- Even when a step fails, the iteration counts as successful as long as the scenario function returns `nil`. Step failures show up in the per-step stats.
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
- `.ExpectStatus(code)`: verify the status code, returns `*jotaro.ExpectError` on mismatch
- `.Err()`: the network error only (HTTP 4xx/5xx are **not** network errors)

**Always finish with `Decode` or `ExpectStatus` (or check `Err()`/`Code`).** A bare `s.Get("/x").Do()` records the request but never marks a wrong status as a failure.

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
| `Arrival: jotaro.Rate(50, time.Second)` | open model: start 50 scenarios per second |
| `Arrival: jotaro.Ramp(0, 100, time.Second, 10*time.Second)` | rate goes from 0 to 100 per second over 10 seconds |
| `Arrival: jotaro.Stages(jotaro.Stage{Arrival: ..., For: ...}, ...)` | several stages in order (ramp up → hold → ramp down) |

In the open model arrivals keep their pace even when the server slows down, and latency is measured from the scheduled start time (Coordinated Omission correction). Use it to find out how the server behaves under a given request rate; use the closed model for a fixed number of concurrent users.

Other `Plan` fields: `MaxUsers` (concurrency cap in the open model, default 1000; arrivals beyond it are counted as dropped), `Timeout` (per request, default 30s), `Grace` (wait for running iterations after `Duration`, default 30s), `Raw` (an `io.Writer` for raw JSON Lines data).

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

See the `jotaro://thresholds` resource for the threshold syntax. A saved JSON result can be judged later with `jotaro check -t 'p95<200ms' result.json`.

## Checklist

1. Every `u.Step` has a distinct, stable name (it is the key in the report and in thresholds).
2. Every request ends with `Decode`, `ExpectStatus`, or an explicit check.
3. Data extracted from one step (tokens, IDs) is declared outside the step closure and checked before the next step uses it.
4. `Plan` sets exactly one of `Users` / `UserStages` / `Arrival`, and a positive `Duration`.
5. Only HTTP can be measured.
6. Do not point the scenario at a server you do not own or are not allowed to load-test.
