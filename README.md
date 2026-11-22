# jotaro

[English](README.md) | [日本語](README_jp.md)

[![CI](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml/badge.svg)](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/RIRUa/jotaro.svg)](https://pkg.go.dev/github.com/RIRUa/jotaro)
[![Release](https://img.shields.io/github/v/release/RIRUa/jotaro)](https://github.com/RIRUa/jotaro/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/RIRUa/jotaro)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Go library for load-testing HTTP servers with scenarios (a flow such as login → list → order). Results can be written to the console, JSON or HTML, and the CLI can check them against thresholds.

- Scenarios are plain Go code (`if` and variables just work)
- Two ways to apply load
  - **Closed**: a fixed number of users repeat the scenario (`Users`), or the number of users changes stage by stage (`UserStages`)
  - **Open**: scenarios start at a fixed rate (`Rate` / `Ramp` / `Stages`). Arrivals keep their pace even when the server slows down, and latency is measured from the scheduled start time (Coordinated Omission correction)
- Measures per-step latency (p50 / p90 / p95 / p99 / max), error breakdown, throughput, TTFB, DNS / TCP / TLS time, and connection reuse rate
- Also records a per-second time series and the health of the load generator itself (CPU, goroutine count)

For the design rationale see [`DESIGN.md`](DESIGN.md); for the result format and the threshold spec see [`docs/result.md`](docs/result.md).

> **Status: v0.x.** We do not change the API without a reason, and we aim to keep existing code working, in the spirit of the [Go 1 compatibility promise](https://go.dev/doc/go1compat). The exception is fixing an existing bug, which can change behavior that code relied on. Such changes are listed in [`CHANGELOG.md`](CHANGELOG.md).

## Try it

Runnable samples that start their own mock server are in [`examples/`](examples/README.md).

```sh
go run ./examples/checkout       # a short flow: login → product list → order
go run ./examples/marketplace    # a deeper scenario with five kinds of visitors
```

## Use as a library

```sh
go get github.com/RIRUa/jotaro
```

Requires Go 1.25 or later.

### 1. Write a scenario

<!-- doccheck: before var creds map[string]string -->
```go
var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
	var login struct {
		Token string `json:"token"`
	}
	if err := u.Step("login", func(s *jotaro.Step) error {
		return s.Post("/login").JSON(creds).Do().Decode(&login)
	}); err != nil {
		return err // return err to stop; ignore it to continue
	}

	u.Think(50 * time.Millisecond) // waiting time; not counted in latency

	return u.Step("order", func(s *jotaro.Step) error {
		return s.Post("/orders").Bearer(login.Token).
			JSON(map[string]int{"item": 1}).
			Do().ExpectStatus(http.StatusCreated)
	})
})
```

- `Step` is the unit of measurement. Results are aggregated per step
- Build a request with `Get` / `Post` / `Put` / `Patch` / `Delete` and send it with `Do()`
- The following can be chained onto a request before `Do()`
  - Headers: `Header(k, v)` / `Headers(map)` / `Bearer(token)` / `Basic(user, pass)`. `Header("Host", v)` (or `Host(v)`) sets the Host that is sent, to select a virtual host or tenant while connecting to the address in `BaseURL`; it does not change the TLS server name
  - Query: `Query(k, v)` / `Queries(url.Values)` (values are escaped). A query written into `Path` is sent as is, so a space or control character in it makes `Do()` return an error and send nothing (the path part itself is escaped by `net/http`)
  - Body: `JSON(v)` / `Form(url.Values)` / `Body(v)` (`[]byte` / `string` / `io.Reader` are sent as is, `url.Values` as a form, anything else as JSON. Set `Content-Type` with `Header` when you need it)
- After `Do()`, chain `Decode(&v)` (read JSON) or one of the `Expect*` checks
  - `ExpectStatus(code)` / `ExpectStatusIn(codes...)`: the status code
  - `ExpectHeader(key, want)` / `ExpectBodyContains(substr)`: a header value / a substring of the body
  - `ExpectJSON(path, want)`: the value at a dot path such as `data.items.0.id`. `Response.JSON(path)` returns that value so you can use it in the next request without defining a struct
  - A failed check returns an error and the step is recorded as `expect_mismatch`. It is not counted as a request error (see `error_rate` under [Threshold syntax](#threshold-syntax))
- To send several requests at once (a page and its API calls, the widgets of a dashboard), use `s.Parallel(fn1, fn2, ...)` inside the step: it runs the functions concurrently, waits for all of them and returns their errors joined (`errors.Join`). Requests of one step may also be sent from your own goroutines, as long as they finish before the step function returns. `u.Step` and `u.Think` must be called from the scenario goroutine only. `Parallel` does not limit how many requests are in flight
- `Think` is think time. Scenario time is reported both without and with `Think`
- Even if a step fails, the iteration counts as successful as long as the scenario function returns `nil`. Step failures show up in the per-step stats
- Cookies are fresh for each iteration. The connection pool is shared by all users

Use a `Feeder` to hand out different data (accounts, etc.) to each user. `LoadCSV` reads one from a CSV.

<!-- doccheck: stmts -->
```go
var accounts = jotaro.NewFeeder([]map[string]string{
	{"user": "alice"}, {"user": "bob"},
}, jotaro.Circular)

creds, ok := accounts.Next()
```

### 2. Choose the load and run

<!-- doccheck: before var checkout *jotaro.Scenario -->
```go
rep, err := jotaro.Run(ctx, jotaro.Plan{
	Scenario: checkout,
	BaseURL:  "http://localhost:8080",
	Duration: time.Minute,
	Users:    20, // closed model. For the open model, use Arrival instead of Users
})
if err != nil && !errors.Is(err, jotaro.ErrAborted) {
	log.Fatal(err) // an invalid Plan (rep is nil), or a failure to write Raw (rep is still returned)
}
```

When `AbortOn` (below) stops a run, `Run` returns the partial report **and** an error that wraps `jotaro.ErrAborted`, so use the report even when `err` is not nil.

Specify exactly one of `Users`, `UserStages` and `Arrival`.

| Setting | Meaning |
|---|---|
| `Users: 20` | 20 users repeat the scenario for `Duration` |
| `UserStages: []jotaro.UserStage{{Target: 100, For: 30*time.Second}, {Target: 100, For: time.Minute}, {Target: 0, For: 10*time.Second}}` | Change the number of users stage by stage: ramp up to 100 over 30 seconds, hold for 1 minute, ramp down to 0 over 10 seconds (the first stage starts from `StartUsers`, 0 by default) |
| `Arrival: jotaro.Rate(50, time.Second)` | Start 50 scenarios per second |
| `Arrival: jotaro.Ramp(0, 100, time.Second, 10*time.Second)` | Increase from 0 to 100 per second over 10 seconds |
| `Arrival: jotaro.Stages(...)` | Switch `Arrival` stage by stage (ramp up → hold → ramp down, etc.) |

To run several flows in one plan, set `Scenarios` instead of `Scenario`: `Scenarios: []jotaro.Weighted{{Scenario: browse, Weight: 7}, {Scenario: checkout, Weight: 3}}`. Each iteration (closed) or arrival (open) picks one of them at random in proportion to the weights, and the report separates the results by scenario name (so the names must differ).

With `UserStages`, a user that is stopped by a lower target finishes the iteration it is running and then waits; it is not interrupted. Users keep their `User.ID` and iteration count when they are started again. The number of concurrent iterations over time is `Series.Concurrency` in the report, so you can check the load shape there.

To repeat a sequence of stages (for example three waves), use `jotaro.RepeatUserStages(3, stage1, stage2, ...)`; it returns the stages repeated 3 times and can be joined with other stages by `append`. Each round starts from the Target the previous round ended at.

You can also set `MaxUsers` (the cap on concurrency in the open model; an arrival that comes in while the cap is reached is not run and not waited for, but counted as dropped, and the text report prints a `warning:` line when that happened), `Timeout` (the limit for a single request), `Grace` (how long to wait for running iterations after `Duration`), `Raw` (where to write raw data), `Progress` (an `io.Writer`, such as `os.Stderr`, that gets one line per second while the run is in progress, e.g. `12s/60s rps=480 p95=38ms err=0.2% 5xx=31.0% fail=31.2%`; `err` is the share of transport-level errors only, `5xx` the share of 5xx responses and `fail` the share of failed iterations such as a failed `Expect*` check, and `5xx`, `fail` and `dropped` are left out while they are zero), and `MetricsAddr` (for example `":9090"`: serve the state of the run so far at `/metrics` in the Prometheus text format, so Grafana and similar tools can chart it during the run; it needs no extra dependency and stops when `Run` returns).

To stop a run as soon as the server is clearly broken, set `AbortOn`, for example `AbortOn: []jotaro.AbortRule{{When: "http_5xx_rate>50%", For: 5 * time.Second}}`. `When` uses the threshold syntax but describes the state to stop at; it is checked every second against the whole run so far, and `For` is how long it must hold. Running iterations get `Grace` to finish, then `Run` returns the partial report together with an error that satisfies `errors.Is(err, jotaro.ErrAborted)`.

### 3. Output the results

<!-- doccheck: before var rep *report.Report -->
```go
rep.Write(os.Stdout, report.FormatText) // FormatText / FormatJSON / FormatHTML
```

You can also judge the results against thresholds.

<!-- doccheck: before var rep *report.Report -->
```go
th, _ := report.ParseThreshold("p95<200ms")
checks := rep.Check(th)
checks.Print(os.Stdout)
if !checks.Passed() {
	os.Exit(1)
}
```

### 4. Lay out the project

A layout that works well for a load test that lives next to the service it tests:

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

When the scenarios grow in number or you run them against several environments, move them into a module of their own.

## CLI

A CLI for processing and judging saved results afterwards.

```sh
go install github.com/RIRUa/jotaro/cmd/jotaro@latest
```

Prebuilt binaries (Linux / macOS / Windows) are attached to each [GitHub Release](https://github.com/RIRUa/jotaro/releases). `jotaro version` prints the version (please include it in bug reports).

Options can come before or after the file names (`jotaro check result.json -t 'p95<200ms'` works), and `--` ends the options. `jotaro help <command>` and `jotaro <command> -h` print the options of a command to standard output. A command that is given the wrong arguments says what is wrong on standard error and exits with code 2.

```sh
# Build an HTML report from a JSON result (default format is html)
jotaro report -o result.html result.json

# Merge several results (e.g. from different machines) into one (default format is json)
jotaro merge -o merged.json a.json b.json

# Check the thresholds. Exit code 1 if any of them is not met
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json

# Compare with a previous run; fail if any p95 got more than 10% worse
jotaro compare -max-p95-regression 10 base.json new.json

# Convert raw data (Plan.Raw output) for other tools
jotaro export -format vegeta -o results.json raw.jsonl   # then: vegeta report -type=json results.json
jotaro export -format csv -o series.csv raw.jsonl        # per-second counts and percentiles
```

- Pass `-` as the input to read from standard input (`report` / `merge` / `check` / `compare` / `export`)
- With `-raw`, the input is read as raw JSON Lines data (the output of `Plan.Raw`) and re-aggregated (`report` / `merge` / `check` / `compare`). `export` always reads raw data. Passing non-raw data to `-raw` or `export` is an error
- Choose the output format of `report` / `merge` with `-format text|json|html`, and that of `export` with `-format vegeta|csv`
- `compare` prints the differences of rps, p50 / p95 / p99 and error rate (overall and per step). With `-max-p95-regression <percent>` it exits with code 1 when a p95 got worse by more than that

### Threshold syntax

`metric[{scenario=name,step=name}] operator value`. Operators are `<` `<=` `>` `>=`.

| Metric | Meaning |
|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | Request latency (`200ms`, `1.5s`) |
| The above prefixed with `corrected_` `ttfb_` `dns_` `connect_` `tls_` | After CO correction / TTFB / DNS / TCP / TLS time |
| `error_rate` | Share of requests that ended in a transport-level error: timeout, connection refused, DNS failure and so on (`1%` or `0.01`). A failed `Expect*` check is not included |
| `http_4xx_rate` `http_5xx_rate` | Share of HTTP 4xx / 5xx |
| `iteration_fail_rate` | Share of failed iterations |
| `iteration_min` … `iteration_max`, `iteration_active_min` … `iteration_active_max` | Latency of a whole scenario iteration, with / without Think time (filter with `{scenario=...}`) |
| `requests` `rps` `dropped` | Number of requests / requests per second / number of dropped arrivals |

A step or scenario name that contains a comma, an unbalanced brace, `<` / `>` or spaces at its ends is written as a Go double-quoted string: `p95{step="list, then order"}<1s` (`\"` and `\\` are escapes).

When the target or the data is missing, the threshold counts as not met, so that a mistyped step name cannot pass silently. See chapter 5 of [`docs/result.md`](docs/result.md) for details.

Two things to know when you choose thresholds:

- `error_rate` is the share of requests that ended in a transport-level error (timeout, connection refused, DNS failure and so on). A failed `Expect*` check is not counted as a request error: it fails the step with `expect_mismatch`, which is listed in the step's errors, and it reaches `iteration_fail_rate` when the scenario returns that error. To fail a run on wrong responses, return the step's error from the scenario (as in the example above) and set `iteration_fail_rate<1%`; use `http_5xx_rate` / `http_4xx_rate` for HTTP error statuses.
- In the open model, when `MaxUsers` is full an arrival is dropped instead of waited for, and a dropped arrival leaves no latency sample. When you use `corrected_*` thresholds, add `dropped<1` so that a run that dropped arrivals cannot pass on the latencies of the arrivals that did run.

## MCP server (scenario authoring and result checks)

`jotaro mcp` starts an [MCP](https://modelcontextprotocol.io) server that helps an AI assistant write scenarios and read saved results. It talks over stdio and never runs a scenario: it only checks code and reads result files.

```sh
claude mcp add jotaro -- jotaro mcp     # Claude Code; other clients take the same command
```

| Kind | Name | Purpose |
|---|---|---|
| Resource | `jotaro://guide` | How to write a scenario: skeleton, API overview, load models, checklist |
| Resource | `jotaro://thresholds` | Threshold syntax and metrics |
| Resource | `jotaro://result` | How to save a result and read it: structure, what each number and error kind means, what to check first |
| Prompt | `write_scenario` | Write a scenario for a described flow, then check it |
| Tool | `check_scenario` | Check a scenario file: Go syntax, jotaro lint (exactly one of `Users` / `UserStages` / `Arrival`, unchecked responses, duplicate or data-built step names, ...) and a type check by compiling. The lint rules match jotaro's types and methods, so they see through aliased imports and variables and ignore your own `Plan` type or `Do()` method; they run once the code compiles, and until then only a name-based fallback that reports warnings runs. Pass `dir` (a module that requires jotaro) to check against its jotaro version; without it a temporary module with the jotaro version of the running `jotaro` binary (the latest one for a development build) is used, which needs network access |
| Tool | `validate_threshold` | Parse a threshold such as `p99{step=login}<100ms` |
| Tool | `summarize_result` | Summarize a saved result file: throughput, error rate, latency and failure reasons per step, dropped arrivals, the worst second, and warnings that say why not to trust it (a saturated load generator, dropped arrivals, too few requests for p95/p99, failed checks that `error_rate` does not show, many 4xx responses, late arrivals, a run that degraded while it ran). Much smaller than the file itself, which holds histograms and a per-second series |
| Tool | `check_result` | Judge a saved result file against thresholds, like `jotaro check`: pass or fail per threshold with the measured value. `raw: true` reads raw JSON Lines data |

## Packages

| Package | Contents |
|---|---|
| `jotaro` | Scenarios (`Scenario` / `User` / `Step`) and load models (`Plan` / `Run` / `Rate` / `Ramp` / `Stages` / `UserStages`). The entry point for users |
| `report` | Aggregation of results (`Report`), output (text / JSON / HTML), merging, comparing, conversion for other tools, and threshold checks |
| `record` | Per-request records and reading/writing raw data (JSON Lines) |
| `cmd/jotaro` | CLI (`report` / `merge` / `check` / `compare` / `export` / `mcp` / `version`) |

## Caveats

- Only HTTP can be measured
- Without `Plan.Raw`, percentiles such as p50 are approximations within 1% relative error (records are folded into histograms as they arrive, so memory does not grow with the run). Count, min, max and mean stay exact. With `Plan.Raw` each record is also written to it as it arrives (still constant memory); to get exact percentiles, re-aggregate the file with `jotaro report -raw`
- There is no statistical comparison (whether two results differ significantly). `check` works by setting criteria and judging pass or fail, and `compare` shows the differences between two results with a gate on the p95 regression only

## License

[MIT](LICENSE)
