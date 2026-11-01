# jotaro

[English](README.md) | [日本語](README_jp.md)

[![CI](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml/badge.svg)](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/RIRUa/jotaro.svg)](https://pkg.go.dev/github.com/RIRUa/jotaro)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Go library for load-testing HTTP servers with scenarios (a flow such as login → list → order). Results can be written to the console, JSON or HTML, and the CLI can check them against thresholds.

- Scenarios are plain Go code (`if` and variables just work)
- Two ways to apply load
  - **Closed**: a fixed number of users repeat the scenario (`Users`)
  - **Open**: scenarios start at a fixed rate (`Rate` / `Ramp` / `Stages`). Arrivals keep their pace even when the server slows down, and latency is measured from the scheduled start time (Coordinated Omission correction)
- Measures per-step latency (p50 / p90 / p95 / p99 / max), error breakdown, throughput, TTFB, DNS / TCP / TLS time, and connection reuse rate
- Also records a per-second time series and the health of the load generator itself (CPU, goroutine count)

For the design rationale see [`DESIGN.md`](DESIGN.md); for the result format and the threshold spec see [`docs/result.md`](docs/result.md).

> **Status: v0.x.** The API may still change between minor versions. Changes are listed in [`CHANGELOG.md`](CHANGELOG.md).

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
  - Headers: `Header(k, v)` / `Headers(map)` / `Bearer(token)` / `Basic(user, pass)`
  - Query: `Query(k, v)` / `Queries(url.Values)` (values are escaped)
  - Body: `JSON(v)` / `Form(url.Values)` / `Body(v)` (`[]byte` / `string` / `io.Reader` are sent as is, `url.Values` as a form, anything else as JSON. Set `Content-Type` with `Header` when you need it)
- After `Do()`, chain `Decode(&v)` (read JSON) or `ExpectStatus(code)` (verify the status)
- `Think` is think time. Scenario time is reported both without and with `Think`
- Even if a step fails, the iteration counts as successful as long as the scenario function returns `nil`. Step failures show up in the per-step stats
- Cookies are fresh for each iteration. The connection pool is shared by all users

Use a `Feeder` to hand out different data (accounts, etc.) to each user. `LoadCSV` reads one from a CSV.

```go
var accounts = jotaro.NewFeeder([]map[string]string{
	{"user": "alice"}, {"user": "bob"},
}, jotaro.Circular)

creds, ok := accounts.Next()
```

### 2. Choose the load and run

```go
rep, err := jotaro.Run(ctx, jotaro.Plan{
	Scenario: checkout,
	BaseURL:  "http://localhost:8080",
	Duration: time.Minute,
	Users:    20, // closed model. For the open model, use Arrival instead of Users
})
```

Specify exactly one of `Users` and `Arrival`.

| Setting | Meaning |
|---|---|
| `Users: 20` | 20 users repeat the scenario for `Duration` |
| `Arrival: jotaro.Rate(50, time.Second)` | Start 50 scenarios per second |
| `Arrival: jotaro.Ramp(0, 100, time.Second, 10*time.Second)` | Increase from 0 to 100 per second over 10 seconds |
| `Arrival: jotaro.Stages(...)` | Switch `Arrival` stage by stage (ramp up → hold → ramp down, etc.) |

You can also set `MaxUsers` (the cap on concurrency in the open model; arrivals beyond it are counted as dropped), `Timeout` (the limit for a single request), `Grace` (how long to wait for running iterations after `Duration`), and `Raw` (where to write raw data).

### 3. Output the results

```go
rep.Write(os.Stdout, report.FormatText) // FormatText / FormatJSON / FormatHTML
```

You can also judge the results against thresholds.

```go
th, _ := report.ParseThreshold("p95<200ms")
checks := rep.Check(th)
checks.Print(os.Stdout)
if !checks.Passed() {
	os.Exit(1)
}
```

## CLI

A CLI for processing and judging saved results afterwards.

```sh
go install github.com/RIRUa/jotaro/cmd/jotaro@latest
```

Prebuilt binaries (Linux / macOS / Windows) are attached to each [GitHub Release](https://github.com/RIRUa/jotaro/releases). `jotaro version` prints the version (please include it in bug reports).

Put options **before** the file names.

```sh
# Build an HTML report from a JSON result (default format is html)
jotaro report -o result.html result.json

# Merge several results (e.g. from different machines) into one (default format is json)
jotaro merge -o merged.json a.json b.json

# Check the thresholds. Exit code 1 if any of them is not met
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json
```

- Pass `-` as the input to read from standard input (`report` / `check`)
- With `-raw`, the input is read as raw JSON Lines data (the output of `Plan.Raw`) and re-aggregated
- Choose the output format with `-format text|json|html`

### Threshold syntax

`metric[{scenario=name,step=name}] operator value`. Operators are `<` `<=` `>` `>=`.

| Metric | Meaning |
|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | Request latency (`200ms`, `1.5s`) |
| The above prefixed with `corrected_` `ttfb_` `dns_` `connect_` `tls_` | After CO correction / TTFB / DNS / TCP / TLS time |
| `error_rate` | Share of network errors and `ExpectStatus` mismatches (`1%` or `0.01`) |
| `http_4xx_rate` `http_5xx_rate` | Share of HTTP 4xx / 5xx |
| `iteration_fail_rate` | Share of failed iterations |
| `requests` `rps` `dropped` | Number of requests / requests per second / number of dropped arrivals |

When the target or the data is missing, the threshold counts as not met, so that a mistyped step name cannot pass silently. See chapter 5 of [`docs/result.md`](docs/result.md) for details.

## MCP server (scenario authoring)

`jotaro mcp` starts an [MCP](https://modelcontextprotocol.io) server that helps an AI assistant write scenarios. It talks over stdio and never runs the scenario it checks.

```sh
claude mcp add jotaro -- jotaro mcp     # Claude Code; other clients take the same command
```

| Kind | Name | Purpose |
|---|---|---|
| Resource | `jotaro://guide` | How to write a scenario: skeleton, API overview, load models, checklist |
| Resource | `jotaro://thresholds` | Threshold syntax and metrics |
| Prompt | `write_scenario` | Write a scenario for a described flow, then check it |
| Tool | `check_scenario` | Check a scenario file: Go syntax, jotaro lint (`Users` xor `Arrival`, unchecked responses, duplicate step names, ...) and a type check by compiling. Pass `dir` (a module that requires jotaro) to check against its jotaro version; without it a temporary module with the latest jotaro is used, which needs network access |
| Tool | `validate_threshold` | Parse a threshold such as `p99{step=login}<100ms` |

## Packages

| Package | Contents |
|---|---|
| `jotaro` | Scenarios (`Scenario` / `User` / `Step`) and load models (`Plan` / `Run` / `Rate` / `Ramp` / `Stages`). The entry point for users |
| `report` | Aggregation of results (`Report`), output (text / JSON / HTML), merging, and threshold checks |
| `record` | Per-request records and reading/writing raw data (JSON Lines) |
| `cmd/jotaro` | CLI (`report` / `merge` / `check` / `mcp`) |

## Caveats

- Only HTTP can be measured
- Percentiles such as p50 are currently computed by sorting all records, which uses memory on long, high-load runs
- There is no statistical comparison (whether two results differ significantly). `check` works by setting criteria and judging pass or fail

## License

[MIT](LICENSE)
