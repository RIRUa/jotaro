# Result specification

[English](result.md) | [日本語](result_jp.md)

Spec notes on jotaro's run results. The **implemented** parts and the **unimplemented (proposed)** parts are written separately.

## 1. Current state (implemented)

A result is an **in-memory `Report` struct**, which can be written to the console, JSON or HTML (chapter 4). Raw data can be written as JSON Lines (see "Raw data" below).

```
Run() ─▶ Collector ─▶ report.BuildReport() ─▶ *report.Report ─▶ print.go ─▶ console
        (raw data)      (aggregation)          (in memory)
```

| Layer | Type | File | Role |
|---|---|---|---|
| Raw data | `record.Record` / `StepRecord` / `IterationRecord` | `record/record.go` | Records per request, step and scenario run |
| Collection | `record.Collector` | `record/record.go` | Receives records concurrently (`Snapshot()` returns a copy) |
| Aggregation | `report.Report` | `report/report.go` | Aggregated results at three levels (request / step / scenario) |
| Output | — | `report/print.go` | Console output |

### Structure of `Report`

```
Report
├─ Open        whether the run used the open model (Rate)
├─ Dropped     number of arrivals not run because MaxUsers was exceeded
├─ Elapsed     run time (slightly longer than Duration)
├─ Requests    RequestStats (all requests combined)
└─ Scenarios[] ScenarioReport
   ├─ Name
   ├─ Iterations  IterationStats (Active = excluding Think / Total = including Think)
   └─ Steps[]     StepStats (in order of first execution)
      └─ Requests RequestStats
```

### `Latency` (distribution of durations)

| Field | Meaning |
|---|---|
| `Count` | Number of samples |
| `Min` / `Mean` / `Max` | Minimum / mean / maximum |
| `P50` `P90` `P95` `P99` | Percentiles (nearest-rank method) |

### `RequestStats`

| Field | Meaning |
|---|---|
| `Count` | Number of requests |
| `Latency` | Latency, including requests that ended in a network error |
| `TTFB` | Only requests that received a response |
| `Corrected` | Time from the scheduled start (after Coordinated Omission correction). Same as `Latency` in the `Users` model |
| `Status` | Counts per status class such as `"2xx"` |
| `Errors` | Breakdown of network errors (per `ErrKind`) |
| `BytesIn` / `BytesOut` | Body bytes (headers are not included) |

### `ErrKind` (kinds of failure)

| Value | Meaning |
|---|---|
| `""` (`ErrNone`) | Success |
| `timeout` | Request timeout |
| `connection_refused` | Connection refused |
| `dns` | Name resolution failure |
| `tls` | TLS failure |
| `canceled` | Aborted, e.g. because `Grace` was exceeded |
| `expect_mismatch` | An expectation such as `ExpectStatus` did not match |
| `other` | Anything else |

HTTP 4xx/5xx are recorded as a status (`Status`), not as an `ErrKind`.

### Notes

- `RPS()` is `Requests.Count / Elapsed`.
- Aggregation sorts all records. If memory becomes a problem on long, high-load runs, this is planned to be replaced with a histogram.

### JSON output (implemented)

`Report.WriteJSON(w io.Writer) error` writes the aggregated result as JSON (`report/json.go`).

```go
f, _ := os.Create("result.json")
defer f.Close()
if err := rep.WriteJSON(f); err != nil {
    log.Fatal(err)
}
```

- All durations are **integer nanoseconds** (field names end in `_ns`).
- The top level has `version` (currently `3`, the constant `JSONVersion`). It is raised whenever a compatibility-breaking change is made. `ReadJSON` can read version 1 and later.
  - `2`: added `started_at` (start time of the run, RFC 3339) and a `buckets` histogram to each `latency`
  - `1`: first version
- There is an output type separate from the internal `Report`, so changing `Report` does not change the JSON shape.
- Empty collections are written as `[]` / `{}` rather than `null` (so readers need no branching).
- `rps` is additionally written (`Requests.Count / Elapsed`). Request aggregation has three entries: `latency` / `ttfb` / `corrected`.

### HTML report (implemented)

`Report.WriteHTML(w io.Writer) error` writes a single self-contained HTML file (`report/html.go` / `report/report.html.tmpl`).

```go
f, _ := os.Create("report.html")
defer f.Close()
if err := rep.WriteHTML(f); err != nil {
    log.Fatal(err)
}
```

- It is generated with `html/template`, so scenario and step names are escaped automatically.
- CSS is embedded; no JS or external resources are used. Light/dark follows the OS setting.
- Contents: summary, breakdown of statuses and errors, latency (p50 to max bars), `corrected` (only for `Rate`), TTFB, per-scenario iteration time and per-step tables.
- Displayed values use the same format as `Print` (`fmtDur`). Use JSON for lossless nanosecond values.
- Sample: `go run ./examples/checkout -format html -o report.html` (`-rate 30` for the `Rate` model).

### Merging reports (implemented)

Combine results into one, e.g. from runs on separate load generators executed at the same time.

```
jotaro merge -o merged.json a.json b.json c.json
jotaro merge -format html -o report.html a.json b.json
```

In the library: `report.MergeReports(reps ...*report.Report) (*report.Report, error)` (`report/merge.go`). The default `-format` is `json` (so the merged result can be used as input again).

| Item | How it is merged |
|---|---|
| Counts, bytes, `Dropped`, statuses, errors | Summed |
| `StartedAt` / `Elapsed` | From the first start to the last end (the maximum of `StartedAt + Elapsed`). RPS is correct whether the runs were simultaneous or sequential |
| `Min` / `Max` / `Mean` | Exact |
| `P50`–`P99` | Recomputed by adding the histograms (**within 1% relative error**) |
| Scenarios and steps | Matched by name. Order is that of first appearance |
| `Open` differs | Error (results from different models cannot be mixed) |

- **Histogram**: each `latency` carries logarithmic buckets (DDSketch style, 1% relative error) (`buckets` in the JSON: bucket number → count). Buckets can be merged by addition, so the result is the same for any number of machines in any order. For a single report, `P50`–`P99` remain exact nearest-rank values as before; they become approximate only after merging.
- **Size**: the number of buckets depends on the spread of the distribution (not proportional to the count). For 100,000 samples of a heavy-tailed distribution, several hundred buckets per `latency`.
- **Connection breakdown (`requests` and each step's `requests`, version 3)**: `dns` / `connect` (TCP) / `tls` are latency distributions (with `buckets`) over only the requests in which that phase actually occurred. Requests that reused a connection, and TLS for http, are not included. `conn_new` / `conn_reused` are the numbers of requests that opened a new connection / reused one; the reuse rate is `conn_reused / (conn_new + conn_reused)`. Requests that failed before obtaining a connection are counted in neither. These are absent from version 1 and 2 JSON (read as 0 / empty).
- **Time series (`series`, version 3)**: an array per second since `started_at`. Elements are `second` / `requests` / `errors` (network errors; HTTP 4xx/5xx not included) / `http_5xx` (number of HTTP 5xx; does not overlap with `errors`; 0 if absent) / `latency` (with `buckets`) / `conn_new` / `conn_reused` (among requests started in that second, those that opened a new connection / reused one) / `dns` / `connect` / `tls` (distributions over only the requests in which that phase occurred; with `buckets`; the same scope as the same-named fields of `requests`) / `concurrency` (the maximum number of running iterations in that second). Empty without `started_at`. When merging, seconds are shifted by the difference in `started_at` and added, and `concurrency` is summed (an upper estimate). Absent from version 1 and 2 JSON. Items from `conn_new` onward were added to version 3 later; JSON without them is read as 0 / empty (compatibility is not broken, so the version was not raised).
- **Load-generator health (`health`, version 3)**: an array per second since `started_at`, for the process running `jotaro.Run`. Elements are `second` / `goroutines` (goroutine count at the end of that second) / `cpu` (the fraction of CPU time used in that second, 0 to 1 relative to all GOMAXPROCS CPUs; -1 on environments where it cannot be obtained). Absent (empty) in results built only with `BuildReport` and in old JSON. When merging, seconds are shifted and overlaid; `goroutines` is summed and `cpu` takes the maximum (with several load generators, look at the most saturated one).
  - CPU is derived from `getrusage` (Unix-like) CPU time. On environments where it cannot be obtained, such as Windows, it is -1 and is excluded from charts and averages. `runtime/metrics` `/cpu/classes/*` is not used because it is updated only at each GC.
  - If the system under test runs in the same process (as in `examples/checkout`), its share is included.
  - When the maximum CPU is 90% or more, the text and HTML summaries show a warning that "the load generator may be saturated and the measurements may look worse than they are".
- **Compatibility with version 1 JSON**: It can be read. Since `buckets` and `started_at` are absent, merges that include it fall back as follows.
  - `P50`–`P99` become a count-weighted average (large error).
  - `Elapsed` becomes the maximum (assuming the runs were simultaneous).

### Raw data (JSON Lines) (implemented)

Pre-aggregation records are written one record per line, so they can be re-aggregated later or visualized with other tools (`record/raw.go`). The aggregated result (`Report`) stays the same size as the count grows, but raw data grows in proportion to the count.

```go
f, _ := os.Create("raw.jsonl")
defer f.Close()
rep, err := jotaro.Run(ctx, jotaro.Plan{ /* ... */ Raw: f }) // written at the end
```

```
go run ./examples/checkout -raw raw.jsonl
jotaro report -raw -format html -o report.html raw.jsonl     # re-aggregate from raw data
jotaro report -raw -format json raw.jsonl | jotaro check -t 'p95<200ms' -   # check via aggregated JSON
jotaro check -raw -t 'p95<200ms' raw.jsonl                    # direct checks also work
```

In the library: `record.WriteRaw(w, *record.Raw)` / `record.ReadRaw(r)` / `report.FromRaw(*record.Raw)`. The result of `Run` also goes through `report.FromRaw`, so a result re-aggregated from the written raw data matches the `Report` returned by `Run`. `report` / `merge` / `check` can read it with `-raw`.

- The first line holds information about the whole run (`type` is `meta`): `version` (`RawVersion`, currently `1`) / `open` / `dropped` / `started_at` / `elapsed_ns`. It is followed by `request` / `step` / `iteration` / `health` lines.
- Durations are **integer nanoseconds** (`_ns`), as in the aggregated JSON. Times are RFC 3339 in UTC. Zero / empty fields are omitted (`code`, `err`, `dns_ns`, `scheduled`, etc.; they return to 0 / empty when read).
- `request` has the same content as `Record`: `scenario` / `step` / `user_id` / `iteration` / `method` / `url` / `code` / `err` / `scheduled` / `start` / `latency_ns` / `conn` / `dns_ns` / `connect_ns` / `tls_ns` / `ttfb_ns` / `bytes_out` / `bytes_in`. `step` is the same as `StepRecord`, `iteration` as `IterationRecord`, and `health` as `HealthPoint`.
- `ReadRaw` skips lines with an unknown `type` (so old readers do not break if a newer version adds lines). It can read without a `meta` line (the run time is derived from the records).
- If raw data is passed to the aggregated-JSON reader (`ReadJSON`), the leading `meta` line can be read as `version: 1` JSON. `ReadJSON` returns an error if other data follows the JSON, so forgetting `-raw` produces the error "use `-raw` for raw data".
- Raw data is written all at once after `Run` finishes (records are kept in memory anyway). It is not written incrementally during the run (which would use no memory).
- Even if writing fails, `Run` returns the aggregated `Report` (together with an `error`).

## 2. Unimplemented (proposals)

> The following are proposals and **not yet decided**. For JSON output items, the implementation in chapter 1 is authoritative. The judgment on the extent of vegeta compatibility under "Open" in `DESIGN.md` is a prerequisite.

### Candidate output formats

| Format | Contents | Pros | Cons |
|---|---|---|---|
| JSON (aggregated) | Output `Report` as is | Small to implement. Easy to check thresholds in CI | Raw data is not kept |
| JSON Lines (raw data; **implemented**) | Output `Record` etc. one per line | Can be re-aggregated and visualized later | Files get large |
| vegeta-compatible | vegeta's `Result` format | Existing `vegeta report` / `plot` can be used | Scenario and step information is lost |

**Recommendation**: JSON (aggregated) first, then raw data (JSON Lines) (both implemented). vegeta compatibility once demand appears.

### What to decide when making JSON

| Issue | Proposal |
|---|---|
| Representation of `time.Duration` | **Store as integer nanoseconds (decided)**. Field names end in `_ns`. Matches Go's standard and is lossless. The display side (console, HTML) converts to milliseconds etc. |
| `map[ErrKind]int` | Keys are strings, so it can be a JSON object as is |
| Output destination | A CLI flag (e.g. `-o result.json`) and the library's `Report.WriteJSON(w io.Writer)` |
| Schema version | Include `"version": 1` to prepare for future changes |

### Output sketch (proposal)

All durations are integer nanoseconds (`_ns`). Field names are tentative.

```json
{
  "version": 1,
  "open": false,
  "dropped": 0,
  "elapsed_ns": 60123400000,
  "requests": {
    "count": 12000,
    "latency": { "count": 12000, "min_ns": 3100000, "mean_ns": 18200000, "p50_ns": 15000000, "p90_ns": 30200000, "p95_ns": 41700000, "p99_ns": 88000000, "max_ns": 240500000 },
    "status": { "2xx": 11990, "5xx": 10 },
    "errors": { "timeout": 3 },
    "bytes_in": 1048576,
    "bytes_out": 204800
  },
  "scenarios": [
    {
      "name": "checkout",
      "iterations": { "count": 4000, "failed": 12 },
      "steps": [ { "name": "login", "count": 4000, "failed": 2 } ]
    }
  ]
}
```

## 3. Ways to view results (proposal)

> A proposal, **not yet decided**. The policy is: use machine-readable result files as the foundation and add ways to view them later.

```
Run ─▶ result file (JSON / JSON Lines) ─▶ static HTML report
                                        ├▶ jq / scripts / CI
                                        └▶ Grafana etc.
Run ─(optional)─▶ /metrics ─▶ Prometheus ─▶ Grafana
```

| Method | Effort | What you can see | Dependencies |
|---|---|---|---|
| JSON + `jq` / CI | Minimal | Final values, threshold checks | None |
| Static HTML report | Small | Distributions, per-step, error breakdown | None (a single file) |
| JSON Lines + external tools | Small | Re-aggregation of raw data, time series | External tools |
| Prometheus + Grafana | Medium | Trends during a run | Infrastructure required |
| Dedicated web UI (server) | Large | Comparing multiple runs, history | Heavy |

### Order of implementation

1. **JSON output** (aggregated results): the foundation of everything. It carries a `version` so other tools do not break when the format changes.
2. **JSON Lines output** (raw data; implemented): for re-aggregation and visualization in other tools.
3. **Static HTML report**: generate a single file from JSON, as in `jotaro report result.json -o report.html`. No server needed, and it can be attached as a CI artifact.
4. **`/metrics`** (Prometheus): add it when you want to see trends during a run.

### Notes on Prometheus integration

- `scenario` / `step` may be labels, but `user_id` and URLs must not be, as the number of series would explode.
- Histograms use fixed buckets, so values drift from nearest-rank percentiles. Bucket boundaries need to be decided.
- Whether to use `client_golang` or write the text format by hand (versus the policy of not adding dependencies) is undecided.
- If the sink for records is made an interface such as `Sink`, output destinations can be added without changing `Run`.

### Caveats

- Embedding a JS library in a static HTML report increases its size. Tables and simple SVG bar charts may be enough at first.
- To see a time series, phase 3 of `DESIGN.md` (per-second aggregation) is needed first. Until then, visualizing final values is the main thing.

## 4. Output API (implemented)

`Report.Write(w io.Writer, f Format) error` is the entry point for output (`report/write.go`). The destination is an `io.Writer`, so a file, standard output, a `bytes.Buffer` or an `http.ResponseWriter` all work.

```
*Report ─▶ Write(w, format) ─▶ Text / JSON / HTML ─▶ w
```

```go
f, _ := report.ParseFormat("json") // from a CLI flag string
if err := rep.Write(os.Stdout, f); err != nil {
    log.Fatal(err)
}
```

| `Format` | Contents | Implementation |
|---|---|---|
| `FormatText` | A human-readable table | `Print` (write failures are returned by `Write` as an `error`) |
| `FormatJSON` | Durations in integer nanoseconds | `WriteJSON` |
| `FormatHTML` | A single self-contained HTML file | `WriteHTML` |

- An unknown format writes nothing and returns an `error`. `ParseFormat` builds a `Format` from a string and returns an `error` if it is unknown.
- If writing fails midway, an `error` is returned. What was already written remains in `w`, so the caller handles that when writing to a file.
- `Print` / `WriteJSON` / `WriteHTML` also stay public. They can be called directly when the format is fixed.
- Sample: `go run ./examples/checkout -format json -o result.json` (omit `-o` for standard output).

### `ReadJSON` and the `report` subcommand (implemented)

A report can be rebuilt later from saved JSON.

```go
rep, err := report.ReadJSON(f) // turn the JSON written by WriteJSON back into a Report
```

- An unsupported `version` (anything not matching `JSONVersion`) returns an `error`.
- A `Report` is unchanged by a round trip (empty maps / slices return to `nil`; confirmed with `reflect.DeepEqual` in tests).
- Durations are integer nanoseconds, so no rounding happens when reading back.

```bash
go run ./examples/checkout -format json -o result.json   # run and save JSON
go run ./cmd/jotaro report -o report.html result.json    # generate HTML from JSON
go run ./cmd/jotaro report -format text result.json      # view as text
cat result.json | go run ./cmd/jotaro report -           # read from standard input
```

| Flag | Default | Meaning |
|---|---|---|
| `-format` | `html` | `text` / `json` / `html` |
| `-o` | standard output | Output file. If writing fails, the partial file is removed |

Exit codes: success `0`, usage error (no arguments, unknown format, etc.) `2`, and read/write failure or an unsupported `version` `1`. Implemented in `cmd/jotaro/main.go`.

### When formats increase (future)

If users want to add their own formats, we will move to an interface instead of a `switch` on `Format`. Until demand appears, we keep the current form.

```go
type Writer interface {
    Write(w io.Writer, r *Report) error
}
```

### Caveats

- Raw data (JSON Lines) is large, so it is not included in `Report.Write` and has separate APIs (`record.WriteRaw`, `Plan.Raw`).

## 5. Threshold checks (implemented)

Check whether a result meets conditions, so that CI can fail when things get slower (`report/threshold.go`). Close to k6's `thresholds`. Checks are made against the `Report` after the run (there is no equivalent of `abortOnFail`, which aborts during a run).

```go
th, _ := report.ParseThreshold("p95<200ms")
checks := rep.Check(th)  // Checks
checks.Print(os.Stdout)
if !checks.Passed() { os.Exit(1) }
```

```
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json
```

Exit code 1 if any threshold is not met. If a threshold is malformed or the file cannot be read, 2 / 1 respectively.

### Syntax

`metric[{scenario=name,step=name}] operator value`. Operators are `<` `<=` `>` `>=`.

| Metric | Meaning | Value | Filter |
|---|---|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | Request latency | Duration (`200ms`, `1.5s`) | `{step=...}` |
| The above prefixed with `corrected_` `ttfb_` `dns_` `connect_` `tls_` | After CO correction / TTFB / DNS / TCP / TLS (e.g. `corrected_p99`) | Duration | `{step=...}` |
| `error_rate` | Share of network errors and Expect mismatches (of the number of requests) | `1%` or `0.01` | `{step=...}` |
| `http_4xx_rate` `http_5xx_rate` | Share of HTTP 4xx / 5xx | Ratio | `{step=...}` |
| `iteration_fail_rate` | Share of failed iterations | Ratio | `{scenario=...}` |
| `requests` | Number of requests | Number | `{step=...}` |
| `rps` `dropped` | Requests per second / number of dropped arrivals | Number | Overall only |

### Decisions

- When `{step=...}` matches same-named steps in several scenarios, each is checked as a separate result (add `scenario=` to narrow it to one).
- **When the target or data is missing, the threshold counts as not met** (a typo in a step name, zero requests, no `ttfb_` records, etc.), so that a misconfiguration or a test that did not run is not treated as a success.
- `error_rate` does not include HTTP 4xx/5xx (a mismatch in a step with `ExpectStatus` is included as `ErrExpect`). 4xx/5xx are viewed separately with `http_4xx_rate` / `http_5xx_rate`.
- There is no scenario-level latency (the request distribution for a whole scenario is not kept). Look at it per step or overall.

### Not supported

- Judging during a run to stop early (`abortOnFail`).
- Writing check results into the result file; `-t` for `report` / `merge`.
- Conditions on the time series (per second).

## 6. Related

- Overall design: [`DESIGN.md`](../DESIGN.md) (measurement in chapter 5, open questions in chapter 8)
- Implementation: `record/record.go` / `report/report.go` / `report/print.go`
