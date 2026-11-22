# jotaro design notes

[English](DESIGN.md) | [日本語](DESIGN_jp.md)

A Go load-testing tool (a library plus a thin CLI) focused on scenario expressiveness.
It collects what was decided in design discussions. Undecided items are at the end.

- Module: `github.com/RIRUa/jotaro`
- Go: 1.25 or later (`go.mod`). Development uses 1.27.3 (pinned in `.mise.toml`). CI tests with 1.25 and the latest stable release

## 1. Motivation

[vegeta](https://github.com/tsenart/vegeta) specializes in firing single requests at a fixed rate and cannot express stateful flows (login → get a token → order → confirm).

Facts checked against the README and `pkg.go.dev` (v12.13.0):

- Targets are independent; chaining, value extraction and scripting are not described in the README.
- `type Targeter func(*Target) error` cannot receive the previous response. `Target` also has no field for a user or a context.
- The unit of `Pacer.Pace(elapsed, hits)` is a single request (hit), so the pace cannot be set in "scenarios per second".
- `Result` is per "a single Target hit" and has no scenario-level success or duration.

→ We do not build on top of vegeta. We build **our own execution engine**.

## 2. Approach

| Item | Decision |
|---|---|
| Form | Library first, plus a thin CLI |
| How scenarios are written | Start with **option A: plain Go functions**. Option B (a builder) is added later as sugar over A |
| Execution model | One user (one run of a scenario) = one goroutine. Steps are delimited with `u.Step(name, fn)`. `User` is used from that goroutine only; requests inside one step may be sent concurrently (`Step.Parallel`), and the scheduled arrival time is taken by exactly one of them |
| Protocol | HTTP only. We first planned to cut interfaces so that others could be plugged in later, but none exists yet (`exec.go` calls `net/http` directly); we will cut one when a second protocol is actually needed |
| YAML | Deferred. If done, one-way: YAML → B (data structures) → A (execution), loaded at run time |
| Request description | Held internally as a data structure (`RequestSpec`: method, path, query, headers, body), to keep the YAML path open. Value extraction and expectations were first planned to live in it too, but as implemented they are methods of `Response` (`Decode`, `JSON(path)`, `Expect*`) |

## 3. How scenarios feel to write (option A)

<!-- doccheck: before type Item struct{}; func creds(id int) any { return nil }; func order(it Item) any { return nil } -->
```go
var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
    var login struct{ Token string `json:"token"` }
    if err := u.Step("login", func(s *jotaro.Step) error {
        return s.Post("/login").JSON(creds(u.ID)).Do().Decode(&login)
    }); err != nil {
        return err
    }

    var items []Item
    u.Step("list", func(s *jotaro.Step) error {
        return s.Get("/items").Bearer(login.Token).Do().Decode(&items)
    })

    u.Think(1 * time.Second)
    return u.Step("order", func(s *jotaro.Step) error {
        return s.Post("/orders").Bearer(login.Token).JSON(order(items[0])).Do().ExpectStatus(201)
    })
})
```

`Scenario` is used as a type name in the implementation, so the constructor is called `NewScenario`.

### Error handling

- `Do()` was first planned to return an `error` only for network errors. As implemented it returns a `*Response`, so that calls chain (`Do().Decode(&v)`): a network error is kept in the response and returned by `Err()`, `Decode`, `JSON` and the `Expect*` methods. HTTP 4xx/5xx are recorded as a status and are not Go errors (so that tests expecting a 404 can be written).
- Expectations are checked explicitly, and an `error` (`*ExpectError`) is returned on mismatch: `ExpectStatus(201)`, `ExpectStatusIn(...)`, `ExpectHeader`, `ExpectBodyContains` and `ExpectJSON(path, want)`. The mismatch is decided after the response has arrived, so it is recorded on the step as `expect_mismatch`, not on the request (see "What `error_rate` counts" in chapter 8).
- Values are read from a response with `Decode(&v)` (into a struct) or `Response.JSON(path)` (one value at a dot path such as `data.items.0.id`, without a struct).
- If `fn` returns an `error`, the step fails. Whether the scenario stops is up to the caller, depending on whether it does `return err`.

### What `User` holds

The user ID, which run it is, an `http.Client` with a cookie jar (new for each iteration), and `Think()`.

## 4. Load models

<!-- doccheck: before var checkout *jotaro.Scenario -->
```go
jotaro.Run(ctx, jotaro.Plan{
    Scenario: checkout,
    Users:    50,                          // closed: a fixed number of concurrent users
    // Arrival: jotaro.Rate(10, time.Second) // open: 10 scenarios per second (mutually exclusive with Users)
    Duration: time.Minute,
})
```

- `Users` (closed): N goroutines run sequentially. **Implemented**.
- `UserStages` (closed, changing number of users): `UserStage{Target, For}` stages move the number of active users linearly from the previous Target (`StartUsers`, 0 by default, at the start) to Target. **Implemented**.
- `Rate` (open): a fixed arrival rate. Reproduces vegeta's strength at the scenario level. **Implemented**. Variable rates are `Ramp` (linear ramp of the rate) and `Stages` (the rate changes stage by stage), both implemented through the same `Arrival` interface.
- `Scenarios` (a weighted mix, instead of `Scenario`): `[]Weighted{Scenario, Weight}`. Each iteration (closed) or arrival (open) picks one scenario at random with probability `Weight / sum of weights`. It is mutually exclusive with `Scenario`, and the scenarios need distinct names because the report and thresholds separate results by scenario name. It is independent of the load model. **Implemented**.

How the `UserStages` model works:

- All users (the largest of `StartUsers` and the Targets) are allocated up front, and `StartUsers` of them run from the very beginning. User `id` is active while the active count is above `id`, so the users with the highest IDs stop first and the lowest start first.
- A controller moves the count at precomputed times: a stage from `a` to `b` users places one single-user step every `For / |b - a|`, so the count reaches Target exactly when the stage ends. After the last stage the count stays at its Target until `Duration`.
- Stopping is cooperative: a user above the count finishes the iteration it is running and then waits (a buffered one-slot channel per user wakes it, so there is no thundering herd). Nothing is interrupted, so `Series.Concurrency` can be above the target for a while when iterations are long.
- `User.ID` and the iteration number are stable for the whole run; a restarted user continues with the next iteration number. The cookie jar is still new for each iteration.
- The load shape is read from `Series.Concurrency` (the peak number of running iterations per second); no separate series is kept.
- `RepeatUserStages(n, stages...)` is only a helper that returns the stages repeated n times as a plain `[]UserStage`. The controller and validation know nothing about repetition, so a repeated shape behaves exactly like the same stages written out by hand.

How the `Rate` model works:

- Arrival times are computed cumulatively from the start time (`i × interval`). Even if the dispatcher falls behind, the interval does not drift; the delayed arrivals are started together.
- An arrival while concurrency is at `MaxUsers` (default 1000) is not run and is counted as a **drop** (`Report.Dropped`). It is not queued or waited for, so a dropped arrival leaves no latency sample: `corrected_*` thresholds should be used together with `dropped<1`.
- The scheduled arrival time is put into `Scheduled` of the first request of an iteration. `Record.Corrected()` (from the scheduled time to the response completion) is the value after Coordinated Omission correction and is aggregated into `RequestStats.Corrected`. For the second and later requests, `Scheduled = Start` (the delay of the previous request is real delay, so it is not corrected).
- Each arrival is treated as a separate user (`User.ID` is the arrival's sequence number and `Iteration` is 0). It ends the same way as the closed model (`Duration` / `Grace`).

How a run ends (all models):

- After `Duration` has passed, no new iterations are started.
- Running iterations are waited for up to `Grace` (default 30 seconds). Those that exceed it are aborted and recorded as `canceled`. A request's own timeout (`Timeout`, default 30 seconds) is `timeout`, which is distinguished from this.
- When the parent `ctx` ends, everything, including running iterations, stops immediately.
- `Plan.AbortOn` ends a run early. Every second the aggregator's snapshot is checked against each rule (a threshold read as "stop when this holds"); a rule that has held for its `For` without a break fires. It then behaves like the end of `Duration` (no new iterations), except that `Grace` is counted from the abort. `Run` returns the partial report with an error wrapping `ErrAborted`, so CI fails by default. The condition covers the whole run so far, not a sliding window, so a late outage moves it slowly; a rule such as `http_5xx_rate>50%` suits a server that is broken from the start.
- As a result `Report.Elapsed` is slightly longer than `Duration`, and RPS comes out slightly low.

Watching a run while it is in progress (all optional, all fed by the same stream of records):

- `Plan.Progress` (an `io.Writer`): one line per second, such as `12s/60s rps=480 p95=38ms err=0.2% 5xx=31.0% fail=31.2%` (`5xx`, `fail` and `dropped` only while they are not zero). It uses a light counter and its own histogram (`progress.go`), so it costs the same with and without `Plan.Raw`.
- `Plan.MetricsAddr` (for example `":9090"`): serves the aggregator's snapshot at `/metrics` in the Prometheus text format (`metrics.go`). The text is written by hand, so no `client_golang` dependency is added; the labels are only the status class and the error kind; the latency is a summary of quantiles, not a fixed-bucket histogram. The server stops when `Run` returns, and a bad address fails the run before any request.
- `Plan.AbortOn`: see above.

### Per-user data (Feeder)

A `Feeder[T]` hands out different data (accounts, input values) to each user. It can be called concurrently from all users.

<!-- doccheck: before var f *os.File -->
```go
rows, _ := jotaro.LoadCSV(f) // first row is the header; a list of maps keyed by column name
accounts := jotaro.NewFeeder(rows, jotaro.Once)

// inside a scenario
acc, ok := accounts.Next()
```

| Mode | Behavior | Suited for |
|---|---|---|
| `Circular` | Hands out in order and wraps to the start at the end | Data that may be reused |
| `Random` | Random every time | Product IDs, etc. |
| `Once` | Returns `false` once exhausted | One account, one use (sign-ups, etc.) |

A Feeder is created outside the scenario and shared through a closure (it is not held by `User`, because different scenarios need different data).

## 5. Measurement

### Structure

**Record raw data per request and aggregate from it.** Aggregation has three levels: request / step (the step name is the aggregation key) / scenario.

The shape of a record carries everything from the start, so that values can be added later without changing the structure.

- Each `httptrace` timing (DNS / TCP / TLS / TTFB)
- Bytes sent and received
- **The scheduled start time and the actual start time are kept separately** (for Coordinated Omission correction)

To keep memory independent of the length of a run, `Run` folds each record into histograms as it arrives (`report.Aggregator`, fed through `record.NewStreamingCollector`) instead of keeping it. The cost is that percentiles become approximate (within 1% relative error); count, min, max and mean stay exact. This holds with `Plan.Raw` too: `Run` writes each record to it as it arrives (`record.RawWriter`) and still keeps nothing, so the `Report` it returns is approximate in that case as well. (It was first planned to keep the records and aggregate exactly with `BuildReport` when `Plan.Raw` is set; that made memory grow with the run.) The exact values are one step away: re-aggregate the written data with `report.FromRaw` (`jotaro report -raw`).

### Phases

| Phase | Contents |
|---|---|
| 1 (MVP, `Users`) | Percentiles (p50/p90/p95/p99/max), error breakdown (timeout / connection refused / DNS / TLS / HTTP 4xx·5xx / `Expect` mismatch, per step), throughput (actual RPS, iterations per second), TTFB, bytes sent and received, scenario time excluding think time |
| 2 (`Rate`) | Number of dropped iterations, Coordinated Omission correction (measuring delay from the scheduled start time). **Implemented** |
| 3 | Time series (per second) and concurrency over time, connection reuse rate and DNS/TCP/TLS time (including their trend over time), health of the load generator (CPU, goroutine count). **Implemented** (aggregation, JSON, merging, HTML charts) |

## 6. MVP scope

**In**: `Scenario` / `User` / `Step` / `Do()` / `ExpectStatus` / `Think` / `Plan` (`Users` only) / console output of per-step latency and error counts (phase 1 measurement)

**Deferred** (at the MVP stage): `Rate` (implemented in phase 2), helpers to extract values from responses (just `Decode` for now; `Response.JSON(path)` came later), vegeta-compatible output (decided in chapter 8 not to build for now; it came later as `jotaro export`), option B (builder), YAML

## 7. Implementation status

Phase 1 (MVP), phase 2 (`Rate`) and phase 3 (time series, connection breakdown, load-generator health) are implemented. You can check it works with `go run ./examples/checkout`.

The library has four packages. The entry point for users is `jotaro` (the side that applies load); the side that handles results is `report`, and records live in `record`. Dependencies are one-way: `jotaro` → `report` → `record` (`jotaro` also depends on `record`), and `internal/histogram` is used by `report` and by `jotaro` (the progress line, `progress.go`). On top of the library there are the CLI (`cmd/jotaro`) and the MCP server behind `jotaro mcp` (`internal/mcpserver`), which are in the table below as well.

| Package | File | Contents |
|---|---|---|
| `jotaro` | `scenario.go` `user.go` `step.go` | `Scenario` / `User` / `Step` (including `Think` and panic recovery) |
| | `request.go` `response.go` `exec.go` | Request description and builder, execution with `httptrace`, error classification |
| | `run.go` | `Plan` / `Run` / `Weighted` (the `Users`, `UserStages` and `Arrival` models; `Scenarios` for a weighted mix; `Plan.Raw` streams raw data) |
| | `abort.go` | `AbortRule` / `ErrAborted` and the once-a-second watcher behind `Plan.AbortOn` |
| | `progress.go` | The once-a-second progress line (`Plan.Progress`) and the fan-out of records to several observers |
| | `metrics.go` | The hand-written `/metrics` endpoint (`Plan.MetricsAddr`) |
| | `user_stages.go` | `UserStage` and the schedule of the active user count |
| | `arrival.go` | `Arrival` / `Rate` / `Ramp` / `Stages` |
| | `feeder.go` | `Feeder` / `LoadCSV` |
| | `health.go` | Samples the health of the load generator (goroutines, CPU) every second |
| `record` | `record.go` | `Record` / `StepRecord` / `IterationRecord` / `Collector` / `Observer` / `NewStreamingCollector` / `ErrKind` / `ConnKind` / `HealthPoint` |
| | `raw.go` | `Raw` / `WriteRaw` / `ReadRaw` (raw data as JSON Lines) |
| | `raw_writer.go` | `RawWriter`: writes raw data record by record while a run is in progress (`Plan.Raw`) |
| `report` | `report.go` `series.go` `aggregate.go` | `Report` / `Latency` and aggregation, time series (per second), incremental aggregation without keeping records (`Aggregator`) |
| | `print.go` `json.go` `html.go` `html_chart.go` `write.go` | Text, JSON and HTML output, and reading JSON |
| | `merge.go` | `MergeReports` |
| | `compare.go` | `Compare` / `Comparison`: diffs of two results and the p95 regression gate (`jotaro compare`) |
| | `export.go` | `WriteVegeta` / `Report.WriteSeriesCSV` (`jotaro export`) |
| | `health.go` | Summarizing and merging load-generator health |
| | `threshold.go` | `Threshold` / `ParseThreshold` / `Report.Check` (threshold checks; [`docs/result.md`](docs/result.md) chapter 5) |
| `internal/histogram` | `histogram.go` | Logarithmic histogram for merging |
| `cmd/jotaro` | `main.go` | CLI (`report` / `merge` / `check` / `compare` / `export` / `mcp` / `version`) |
| `internal/mcpserver` | `server.go` `resources.go` `tools.go` `check.go` `lint.go` `result.go` `summary.go` `prompts.go` | MCP server behind `jotaro mcp` (scenario authoring: guide resources, `check_scenario`, `validate_threshold`; reading saved results: `summarize_result`, `check_result`) |

Decisions made during implementation:

- The cookie jar is renewed for each iteration (same as k6's default; log in again every time). The connection pool is shared by all users.
- `BuildReport` (used by `FromRaw` and by library users) computes percentiles by sorting all records (nearest-rank). Copies during aggregation are avoided (records are held by position instead of copied, temporary slices are sorted in place, and `Collector.Take` hands over the records of a keeping `Collector` without copying). When `Run` still used `BuildReport`, this took the allocation during aggregation for 1 million requests from 1.4GB to 0.15GB. The records themselves (about 240MB per million) are no longer kept by `Run`, with or without `Plan.Raw`: the `Aggregator` folds them into histograms instead (and `RawWriter` writes them out at once). For 5 million requests the heap held by the collector went from about 1165MB to about 9MB in a measurement on one machine. The concurrency of the time series is tracked as iterations start and end (one counter and one maximum per second), not swept from stored events.
- Request latency includes requests that ended in a network error (timeout, etc.). TTFB covers only those that received a response.
- `BytesIn` / `BytesOut` are body bytes (headers are not included).

## 8. Decisions and open questions

### Decided

- **An abstraction close to `Pacer`:** We have our own `Arrival` (vegeta's `lib` is not imported), to avoid adding dependencies and to control things at the scenario level. Variable rates are handled by adding `Arrival` implementations (`Ramp` for a linear ramp-up and `Stages` for staged load are implemented).
- **Scenario time including think time:** Report both (implemented). `IterationStats` holds both `Active` (excluding `Think`) and `Total` (including it), and both appear in the console, JSON and HTML.
- **Behavior when a step fails:** No helper is provided. `u.Step` returns `fn`'s error as is, and the failure remains in the `StepRecord`. To stop, `return err`; to continue, ignore it. Being able to write it with plain Go `if` is the advantage of option A, and a helper would add an implicit rule about which is the default.
  - Whether an iteration succeeded is decided by the error the scenario function returned. If a step fails but the scenario continues to the end and returns `nil`, the iteration counts as successful (step failures are seen in the per-step aggregate `StepStats.Failed`).
  - When option B (builder) is built, the default for a sequence of steps (continue / stop) will need to be decided. We will revisit it then.
- **vegeta compatibility:** At first we decided not to build it for now. Turning results into vegeta's `Result` would lose scenario and step information. Once raw data (JSON Lines; [`docs/result.md`](docs/result.md)) could be written, we decided to handle it with a subcommand that converts raw data into vegeta's format, without using `lib` types directly (for the same reason as `Pacer`: not adding dependencies). It was then implemented as decided: `jotaro export -format vegeta` (and `-format csv` for the per-second series), `report.WriteVegeta`. The attack name is `<scenario>/<step>`, since the format cannot carry more.
- **What `error_rate` counts:** Only requests that ended in a transport-level error (timeout, connection refused, DNS failure and so on). A failed `Expect*` check is decided after the response has arrived, so it belongs to the step: it fails the step with `expect_mismatch` and reaches `iteration_fail_rate` when the scenario returns the error. The documents had said that `error_rate` included Expect mismatches, which the implementation never did. For v0.1.3 we kept the implementation and corrected the documents. To fail a run on wrong responses, return the step's error from the scenario and put a threshold on `iteration_fail_rate`.

### Open

- **How far to keep value passing type-safe:** With A alone (Go variables and `Decode`) there is no problem. B and YAML would use string keys, so we will decide when building B or YAML.
