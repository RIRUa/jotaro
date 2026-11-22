# Result specification

[English](result.md) | [日本語](result_jp.md)

Spec notes on jotaro's run results. Everything described here is implemented. Chapters 2 and 3 keep the reasoning behind the output formats and the ways to view results (how they were decided); chapter 1 and chapters 4 to 6 describe how it works now.

## 1. Current state (implemented)

A result is an **in-memory `Report` struct**, which can be written to the console, JSON or HTML (chapter 4). Raw data can be written as JSON Lines (see "Raw data" below).

```
                       ┌─▶ report.Aggregator ─▶ *report.Report ─▶ Write ─▶ console / JSON / HTML
Run() ─▶ Collector ────┤     (also read by AbortOn and /metrics)
 (each record as       ├─▶ record.RawWriter ─▶ Plan.Raw (JSON Lines)
  it arrives)          └─▶ progress counter ─▶ Plan.Progress
```

`Run` does not keep the records. The `Collector` hands each one to its observers (`record.Observer`) as soon as it arrives, so memory does not grow with the length of the run.

| Layer | Type | File | Role |
|---|---|---|---|
| Raw data | `record.Record` / `StepRecord` / `IterationRecord` | `record/record.go` | Records per request, step and scenario run |
| Collection | `record.Collector` | `record/record.go` | Receives records concurrently. `NewStreamingCollector` (used by `Run`) hands each record to a `record.Observer` and keeps nothing; `NewCollector` keeps them (`Snapshot()` returns a copy) |
| Aggregation | `report.Report` | `report/report.go` | Aggregated results at three levels (request / step / scenario). Built incrementally by `report.Aggregator` (`report/aggregate.go`, used by `Run`), or from a full set of records by `report.BuildReport` / `FromRaw` |
| Output | — | `report/print.go` / `json.go` / `html.go` | Console, JSON and HTML output |
| Raw data writing | `record.RawWriter` | `record/raw_writer.go` | Writes each record to `Plan.Raw` as it arrives |

### Structure of `Report`

```
Report
├─ Open        whether the run used the open model (Arrival: Rate / Ramp / Stages)
├─ Dropped     number of arrivals not run because MaxUsers was reached
├─ StartedAt   start time of the run (UTC; zero if unknown)
├─ Elapsed     run time (slightly longer than Duration)
├─ Requests    RequestStats (all requests combined)
├─ Scenarios[] ScenarioReport
│  ├─ Name
│  ├─ Iterations  IterationStats (Active = excluding Think / Total = including Think)
│  └─ Steps[]     StepStats (in order of first execution): Count, Failed, Duration, Errors (why the step failed, per ErrKind)
│     └─ Requests RequestStats
├─ Series[]    SeriesPoint: per-second aggregate since StartedAt (see "Merging reports")
└─ Health[]    record.HealthPoint: per-second health of the load generator (only for a run done with Run)
```

### `Latency` (distribution of durations)

| Field | Meaning |
|---|---|
| `Count` | Number of samples |
| `Min` / `Mean` / `Max` | Minimum / mean / maximum |
| `P50` `P90` `P95` `P99` | Percentiles. Exact (nearest-rank, an observed value) for `BuildReport` / `FromRaw`; approximate (within 1% relative error, clamped to `[Min, Max]`, generally not an observed value) for `Run` and `MergeReports`. See "Notes" below |

### `RequestStats`

| Field | Meaning |
|---|---|
| `Count` | Number of requests |
| `Latency` | Latency, including requests that ended in a network error |
| `TTFB` | Only requests that received a response |
| `DNS` / `Connect` / `TLS` | Time of each phase of opening a connection, over only the requests in which that phase actually happened |
| `NewConns` / `ReusedConns` | `NewConns` is the number of connections opened, counted on the request that used each one first (the number of connections that carried at least one request); `ReusedConns` is the number of requests that used a connection an earlier request had already used. A connection that was dialed but never carried a request is in neither, so a server can have accepted more connections than `NewConns`: connections still idle when the run ended, failed dials, and with HTTP/2 the extra connections `net/http` dials at start-up and closes unused. With HTTP/2 `NewConns` is small because one connection serves many requests |
| `Corrected` | Time from the scheduled start (after Coordinated Omission correction). Same as `Latency` in the closed model (`Users` / `UserStages`) |
| `Status` | Counts per status class such as `"2xx"` |
| `Errors` | Breakdown of transport-level errors (per `ErrKind`). Failed `Expect*` checks are not in it (they are in the `Errors` of the step) |
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
| `expect_mismatch` | An `Expect*` check (`ExpectStatus`, `ExpectJSON`, ...) did not match. Only steps and iterations have it |
| `panic` | The scenario function panicked (a bug in the test code, never a property of the target). Only steps and iterations have it |
| `other` | Anything else |

HTTP 4xx/5xx are recorded as a status (`Status`), not as an `ErrKind`. A request can have both: if the status line arrived but reading the body then failed (for example a timeout), the record has its `Code` and an `ErrKind`.

Where each kind is recorded: the transport-level kinds (`timeout`, `connection_refused`, `dns`, `tls`, `canceled`, `other`) are recorded on the request, and also on the step and the iteration when the error is returned from them. `panic` is recorded on the step that was running and on the iteration: the step is listed as failed (`Failed`) with the time up to the panic, requests sent before the panic stay under it, and the run goes on with the other users. The first panic of a run is written once, with its stack, to `Plan.Progress` (or to standard error when `Progress` is nil) after the run; later ones are only counted. `expect_mismatch` is decided by the `Expect*` call after the response has arrived, so it is **not** recorded on the request: it appears in `Errors` of the step (and of the iteration, when the scenario returns the error), and it fails the step (`Failed`). That is why `error_rate` (chapter 5) does not see it.

A step's `Errors` is the reason of each failed run, one `ErrKind` per run, so the counts add up to `Failed`.

Where the reasons are shown: the text report lists them in the `failures` column of "steps (duration)" (`expect_mismatch=500`), and the HTML report in the same column of its step table. The `errors` column of "steps (requests)" is named `net errors` in both, because it counts only the transport-level errors of the requests; a step whose checks failed shows `fail` above 0, `failures` with `expect_mismatch` and `net errors` as `-`. `summarize_result` of `jotaro mcp` returns the same breakdown as `errors` in each step, and its `failed_checks` warning names the kinds. A step with no failure shows `-` in `failures`.

### Notes

- `RPS()` is `Requests.Count / Elapsed`.
- `Run` aggregates incrementally (`report.Aggregator`), with or without `Plan.Raw`: each record is folded into counters and log histograms as it arrives and is not kept, so memory does not grow with the number of requests. Count / Min / Max / Mean are exact, and `P50`–`P99` are approximations within 1% relative error (clamped to `[Min, Max]`).
- `FromRaw` and `BuildReport` receive every record and sort them, so `P50`–`P99` are exact nearest-rank values. To get exact percentiles for a run, write `Plan.Raw` and re-aggregate the file (`jotaro report -raw`).

### JSON output (implemented)

`Report.WriteJSON(w io.Writer) error` writes the aggregated result as JSON (`report/json.go`).

<!-- doccheck: before var rep *report.Report -->
```go
f, _ := os.Create("result.json")
defer f.Close()
if err := rep.WriteJSON(f); err != nil {
    log.Fatal(err)
}
```

- All durations are **integer nanoseconds** (field names end in `_ns`).
- The top level has `version` (currently `3`, the constant `JSONVersion`). It is raised whenever a compatibility-breaking change is made. `ReadJSON` can read version 1 and later.
  - `3`: added `series` (the per-second time series) and, in `requests`, `dns` / `connect` / `tls` / `conn_new` / `conn_reused`. The connection fields of `series` and `health` (the health of the load generator) were added to version 3 later (JSON without them is read as 0 / empty), so the version was not raised
  - `2`: added `started_at` (start time of the run, RFC 3339) and a `buckets` histogram to each `latency`
  - `1`: first version
- There is an output type separate from the internal `Report`, so changing `Report` does not change the JSON shape.
- Empty collections are written as `[]` / `{}` rather than `null` (so readers need no branching).
- `rps` is additionally written (`Requests.Count / Elapsed`). Request aggregation has six distributions, `latency` / `ttfb` / `corrected` / `dns` / `connect` / `tls`, and the counts `conn_new` / `conn_reused`.

### HTML report (implemented)

`Report.WriteHTML(w io.Writer) error` writes a single self-contained HTML file (`report/html.go` / `report/report.html.tmpl`).

<!-- doccheck: before var rep *report.Report -->
```go
f, _ := os.Create("report.html")
defer f.Close()
if err := rep.WriteHTML(f); err != nil {
    log.Fatal(err)
}
```

- It is generated with `html/template`, so scenario and step names are escaped automatically.
- CSS is embedded; no JS, chart library or external resources are used (the charts are inline SVG). Light/dark follows the OS setting.
- The page declares `<html lang="en">` (its text is English). The start time (UTC) is shown in the header and in `<title>`, so two reports can be told apart; both are left out when the start time is unknown.
- Contents: summary (including the load model, `closed` / `open`), breakdown of statuses and errors, latency (p50 to max bars), `corrected` (open model only, that is, with `Arrival`), TTFB, per-scenario iteration time and per-step tables (with the failure reasons of each step in `failures`). When the report has a time series, a per-second timeline of inline SVG line charts follows: requests / errors / 5xx, latency p50 / p95 / p99, concurrency, connections, DNS / connect / TLS time, and the goroutines and CPU of the load generator.
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

- **Histogram**: each `latency` carries logarithmic buckets (DDSketch style, 1% relative error) (`buckets` in the JSON: bucket number → count). Buckets can be merged by addition, so the result is the same for any number of machines in any order. For a report built from all records (`BuildReport`, `FromRaw`), `P50`–`P99` are exact nearest-rank values; for one built incrementally (`Run`, with or without `Plan.Raw`) and for merged reports they are approximate.
- **Size**: the number of buckets depends on the spread of the distribution (not proportional to the count). For 100,000 samples of a heavy-tailed distribution, several hundred buckets per `latency`.
- **Connection breakdown (`requests` and each step's `requests`, version 3)**: `dns` / `connect` (TCP) / `tls` are latency distributions (with `buckets`) over only the requests in which that phase actually occurred. Requests that reused a connection, and TLS for http, are not included. `conn_new` is the number of connections opened, counted on the request that used each one first, and `conn_reused` the number of requests that used a connection an earlier request had already used; the reuse rate is `conn_reused / (conn_new + conn_reused)`. `net/http`'s own "reused" flag is not used: it also marks a connection that was dialed for one request but parked in the idle pool and first used by another (common over TLS, where the handshake is slow), which made `conn_new` understate the connections opened. A connection that was dialed but never carried a request is in neither (one still idle when the run ended; with HTTP/2, the extra connections `net/http` dials at start-up and closes unused), so a server can have accepted more connections than `conn_new`; a failed dial appears as an error. Counting every dialed connection is tracked in [#184](https://github.com/RIRUa/jotaro/issues/184). The `dns` / `connect` / `tls` distributions stay with the request that started the dial, which may not be the request counted in `conn_new`. Requests that failed before obtaining a connection are counted in neither. These are absent from version 1 and 2 JSON (read as 0 / empty).
- **Time series (`series`, version 3)**: an array per second since `started_at`. Elements are `second` / `requests` / `errors` (network errors; HTTP 4xx/5xx not included) / `http_5xx` (number of HTTP 5xx; it can overlap with `errors` when the status line was 5xx but reading the body then failed; 0 if absent) / `latency` (with `buckets`) / `conn_new` / `conn_reused` (among requests started in that second, the connections first used by one of them / the requests that used an already used connection; see above) / `dns` / `connect` / `tls` (distributions over only the requests in which that phase occurred; with `buckets`; the same scope as the same-named fields of `requests`) / `concurrency` (the maximum number of running iterations in that second). Empty without `started_at`. When merging, seconds are shifted by the difference in `started_at` and added, and `concurrency` is summed (an upper estimate). Absent from version 1 and 2 JSON. Items from `conn_new` onward were added to version 3 later; JSON without them is read as 0 / empty (compatibility is not broken, so the version was not raised).
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
rep, err := jotaro.Run(ctx, jotaro.Plan{ /* ... */ Raw: f }) // written while the run is in progress
```

```
go run ./examples/checkout -raw raw.jsonl
jotaro report -raw -format html -o report.html raw.jsonl     # re-aggregate from raw data
jotaro report -raw -format json raw.jsonl | jotaro check -t 'p95<200ms' -   # check via aggregated JSON
jotaro check -raw -t 'p95<200ms' raw.jsonl                    # direct checks also work
```

In the library: `record.WriteRaw(w, *record.Raw)` / `record.RawWriter` (streaming) / `record.ReadRaw(r)` / `report.FromRaw(*record.Raw)`. Counts, errors and the time series of a result re-aggregated from the written raw data match the `Report` returned by `Run`; only `P50`–`P99` differ, by at most 1%, because `Run` aggregates incrementally while `FromRaw` computes them exactly. `report` / `merge` / `check` / `compare` can read it with `-raw`, and `export` (below) always reads raw data. Passing data that is not raw (an aggregated result) to `-raw` or to `export` is an error.

- A `meta` line (`type` is `meta`) holds information about the whole run: `version` (`RawVersion`, currently `1`) / `open` / `dropped` / `started_at` / `elapsed_ns`. `record.WriteRaw` writes everything in the first line. `Run` writes the first one at the start (`open`, `started_at`) and a second one at the end with the final `dropped` and `elapsed_ns` (see below). `request` / `step` / `iteration` / `health` lines come in between and after.
- Durations are **integer nanoseconds** (`_ns`), as in the aggregated JSON. Times are RFC 3339 in UTC. Zero / empty fields are omitted (`code`, `err`, `dns_ns`, `scheduled`, etc.; they return to 0 / empty when read).
- `request` has the same content as `Record`: `scenario` / `step` / `user_id` / `iteration` / `method` / `url` / `code` / `err` / `scheduled` / `start` / `latency_ns` / `conn` / `dns_ns` / `connect_ns` / `tls_ns` / `ttfb_ns` / `bytes_out` / `bytes_in`. `step` is the same as `StepRecord`, `iteration` as `IterationRecord`, and `health` as `HealthPoint`.
- `ReadRaw` skips lines with an unknown `type` (so old readers do not break if a newer version adds lines). It can read without a `meta` line (the run time is derived from the records).
- If raw data is passed to the aggregated-JSON reader (`ReadJSON`), the leading `meta` line can be read as `version: 1` JSON. `ReadJSON` returns an error if other data follows the JSON, so forgetting `-raw` produces the error "use `-raw` for raw data". In the other direction, passing an aggregated result to `-raw` or `export` is an error as well.
- `Run` writes raw data incrementally (`record.RawWriter`): each record is written as it arrives, so memory stays constant however many requests there are. Lines are therefore in arrival order (requests, steps and iterations are interleaved), followed by the `health` lines. The stream starts with a `meta` line (`open`, `started_at`) and ends with a second `meta` line that has the final `dropped` and `elapsed_ns`; `ReadRaw` takes the last value of each field. The buffer is flushed at most one second after a record is written, so the file can be followed live and a run that dies loses at most about a second of records. A run that dies before the end still leaves a readable file: `ReadRaw` returns the records before a cut last line and sets `Raw.Truncated`, and the run time is derived from the records. `jotaro report` / `merge` / `check` / `compare -raw` and `export` print a warning to stderr in that case. A broken line anywhere but the end is still an error.
- Because the output is written during the run, an aborted or cancelled run leaves what was written so far.
- Even if writing fails, `Run` returns the aggregated `Report` (together with an `error`).

## 2. Output formats: how they were chosen (implemented)

> This chapter keeps the reasoning behind the output formats, as it was written before they were built. All of it has since been implemented. For the JSON schema, the implementation in chapter 1 is authoritative.

### Candidate output formats

| Format | Contents | Pros | Cons |
|---|---|---|---|
| JSON (aggregated; **implemented**) | Output `Report` as is | Small to implement. Easy to check thresholds in CI | Raw data is not kept |
| JSON Lines (raw data; **implemented**) | Output `Record` etc. one per line | Can be re-aggregated and visualized later | Files get large |
| vegeta-compatible (**implemented** as `jotaro export -format vegeta`) | vegeta's `Result` format | Existing `vegeta report` / `plot` can be used | Scenario and step information is lost |

**Order taken**: JSON (aggregated) first, then raw data (JSON Lines), then vegeta compatibility once demand appeared. The vegeta format is produced from raw data by `jotaro export` (chapter 4), not as an output format of `Report`, because it cannot carry the scenario and step information that a `Report` is built on.

### What was decided for JSON

| Issue | Decision |
|---|---|
| Representation of `time.Duration` | **Store as integer nanoseconds**. Field names end in `_ns`. Matches Go's standard and is lossless. The display side (console, HTML) converts to milliseconds etc. |
| `map[ErrKind]int` | Keys are strings, so it can be a JSON object as is |
| Output destination | A CLI flag (e.g. `-o result.json`) and the library's `Report.WriteJSON(w io.Writer)` |
| Schema version | Include `version` to prepare for future changes. It started at `1` and is `3` now (see chapter 1) |

### Output sketch (the first proposal, version 1)

This is the first sketch, kept as it was written. The real output has more fields (for example `started_at`, `buckets`, `series` and `health`); see chapter 1.

All durations are integer nanoseconds (`_ns`). Field names were tentative.

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

## 3. Ways to view results (implemented)

The policy is: use machine-readable result files as the foundation and add ways to view them later. Everything below exists except the dedicated web UI.

```
Run ─▶ result file (JSON / JSON Lines) ─▶ static HTML report
                                        ├▶ jq / scripts / CI
                                        └▶ Grafana etc. (via jotaro export)
Run ─(Plan.MetricsAddr)─▶ /metrics ─▶ Prometheus ─▶ Grafana
Run ─(Plan.Progress)─▶ one line per second on the console
```

| Method | Effort | What you can see | Dependencies | Status |
|---|---|---|---|---|
| JSON + `jq` / CI | Minimal | Final values, threshold checks | None | Implemented |
| Static HTML report | Small | Distributions, per-step, error breakdown, per-second charts | None (a single file) | Implemented |
| JSON Lines + external tools | Small | Re-aggregation of raw data, time series | External tools | Implemented (`jotaro export`) |
| Progress line | Minimal | Throughput, p95 and error rate during a run | None | Implemented (`Plan.Progress`) |
| Prometheus + Grafana | Medium | Trends during a run | Infrastructure required | Implemented (`Plan.MetricsAddr`) |
| Dedicated web UI (server) | Large | Comparing multiple runs, history | Heavy | Not implemented |

### Order of implementation (all done)

1. **JSON output** (aggregated results): the foundation of everything. It carries a `version` so other tools do not break when the format changes.
2. **JSON Lines output** (raw data): for re-aggregation and visualization in other tools.
3. **Static HTML report**: generate a single file from JSON, as in `jotaro report result.json -o report.html`. No server needed, and it can be attached as a CI artifact.
4. **`/metrics`** (Prometheus): to see trends during a run.

### Progress line (`Plan.Progress`) (implemented)

If `Plan.Progress` is an `io.Writer` (for example `os.Stderr`), `Run` writes one line to it every second while the run is in progress:

```
12s/60s rps=480 p95=38ms err=0.2% 5xx=31.0% fail=31.2% dropped=7
```

The fields are the elapsed time / `Duration` and the throughput of the last second, followed by values over the run so far, which converge to the ones of the final report:

| Field | Meaning | Same as |
|---|---|---|
| `p95` | 95th percentile of the request latency (an approximation within 1% relative error) | `p95` |
| `err` | share of requests that ended in a transport-level error (a timeout, a refused connection and so on). A request that got a response is never counted here, whatever its status, so a server that answers every request with `500`, or with a body that fails an `Expect*` check, still shows `err=0.0%` | `error_rate` |
| `5xx` | share of requests that got a 5xx response. A request that ended in a transport-level error is not counted here | `http_5xx_rate` |
| `fail` | share of finished iterations that ended with an error of any kind, such as the error that a failed `Expect*` check returns from the scenario | `iteration_fail_rate` |
| `dropped` | number of arrivals dropped because of `MaxUsers` (open model) | `dropped` |

`5xx`, `fail` and `dropped` are left out while they are zero, so a healthy line stays short; a line that shows only `err=0.0%` means no 5xx response and no failed iteration yet, not only no transport error. A failed `Expect*` check that the scenario does not return (it ignores the error) is not seen by `fail`. It works with and without `Plan.Raw`, adds no recording cost, ignores write errors, and writes nothing after `Run` returns. If `AbortOn` stops the run, a line `aborted: ...` follows (see "Stopping a run early" in chapter 5).

### `/metrics` (`Plan.MetricsAddr`) (implemented)

If `Plan.MetricsAddr` is set (for example `":9090"`), `Run` serves the state of the run so far at `http://<MetricsAddr>/metrics` in the Prometheus text format (version 0.0.4), so Prometheus and Grafana can chart it while the run is in progress. The server stops when `Run` returns. If the address cannot be listened on, `Run` fails before sending any request.

```go
rep, err := jotaro.Run(ctx, jotaro.Plan{ /* ... */ MetricsAddr: ":9090" })
```

| Metric | Type | Meaning |
|---|---|---|
| `jotaro_requests_total` | counter | Requests finished so far |
| `jotaro_dropped_total` | counter | Arrivals not run because of the `MaxUsers` cap |
| `jotaro_bytes_received_total` / `jotaro_bytes_sent_total` | counter | Body bytes received / sent |
| `jotaro_responses_total{class="2xx"}` | counter | Responses by status class |
| `jotaro_errors_total{kind="timeout"}` | counter | Transport-level errors by `ErrKind` |
| `jotaro_elapsed_seconds` | gauge | Seconds since the run started |
| `jotaro_requests_per_second` | gauge | Average throughput over the run so far |
| `jotaro_request_duration_seconds{quantile="0.95"}` (0.5 / 0.9 / 0.95 / 0.99), `_sum`, `_count` | summary | Request latency over the run so far (within 1% relative error) |

Decisions made when it was built:

- The text format is written by hand, so no dependency such as `client_golang` is added.
- The labels are only the status class and the error kind. `scenario` / `step` are not exposed, and `user_id` and URLs never will be, as the number of series would explode.
- The latency is a summary of quantiles computed from the same incremental aggregation as the report, not a fixed-bucket histogram, so it does not drift from the report because of bucket boundaries. The values are for the whole run so far, not a sliding window.
- The interface that was proposed here as a `Sink` exists as `record.Observer`: `Run` passes each record to the aggregator, the raw data writer and the progress counter through it, so output destinations can be added without changing the recording code.

### Notes

- The static HTML report embeds no JS library. Tables, bars and simple inline SVG line charts turned out to be enough.
- The per-second time series (`series`, chapter 1) is implemented, so the timeline is in the HTML report and in the CSV of `jotaro export -format csv` (chapter 4).

## 4. Output API (implemented)

`Report.Write(w io.Writer, f Format) error` is the entry point for output (`report/write.go`). The destination is an `io.Writer`, so a file, standard output, a `bytes.Buffer` or an `http.ResponseWriter` all work.

```
*Report ─▶ Write(w, format) ─▶ Text / JSON / HTML ─▶ w
```

<!-- doccheck: before var rep *report.Report -->
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

<!-- doccheck: before var f *os.File -->
```go
rep, err := report.ReadJSON(f) // turn the JSON written by WriteJSON back into a Report
```

- A `version` that is not supported (outside 1 to `JSONVersion`, that is, 1 to 3 can be read) returns an `error`.
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
| `-raw` | off | Read the input as raw data (JSON Lines, the output of `Plan.Raw`) and re-aggregate it. `merge`, `check` and `compare` have it too |

Exit codes: success `0`, usage error (no arguments, unknown format, etc.) `2`, and read/write failure or an unsupported `version` `1`. Implemented in `cmd/jotaro/main.go`.

### Comparing two results: `compare` (implemented)

```
jotaro compare [-raw] [-max-p95-regression <percent>] <base.json> <new.json>
```

It prints a table of what changed from the base result to the new one: `rps` overall, and `p50` / `p95` / `p99` / `error_rate` overall and for each step that exists in both (matched by scenario and step name; a metric with no data in either run is left out). The CHANGE column is the relative change, and `worse` / `better` marks the direction (for `rps`, higher is better). `error_rate` is the same as in chapter 5: transport-level errors only.

With `-max-p95-regression <percent>`, the exit code is 1 when any p95 (overall or of a step) got worse by more than that percentage, with a `FAIL ...` line for each. Only the p95 is gated; the other rows are for reading. There is no statistical test of whether the difference is significant. With `-raw`, both inputs are read as raw data. In the library: `report.Compare(base, cur)` returns a `Comparison` (`Print`, `Violations(maxPct)`). Exit codes: `0`, `1` (a regression over the limit, or an unreadable input), `2` (usage error).

### Converting raw data: `export` (implemented)

```
jotaro export -format vegeta|csv [-o output] <raw.jsonl | ->
```

It reads raw data (the output of `Plan.Raw`; it never reads an aggregated result) and converts it for other tools.

- `vegeta`: one JSON object per request, ordered by start time, in vegeta's result encoding, so `vegeta report -type=json` and `vegeta plot` can read it. The format cannot carry scenario and step details, so the attack name is `<scenario>/<step>`. A transport-level error has code 0 and the error kind as its message; HTTP 4xx/5xx keep their status code. In the library: `report.WriteVegeta(w, raw)`.
- `csv`: the per-second time series with a header row: `second`, `requests`, `errors`, `http_5xx`, `new_conns`, `reused_conns`, `concurrency`, and `latency_min_ms` / `mean` / `p50` / `p90` / `p95` / `p99` / `max` (milliseconds with a fraction, so spreadsheets can use them directly). In the library: `Report.WriteSeriesCSV(w)`.

`-o` is the output file (standard output if omitted; if writing fails, the partial file is removed). Exit codes: `0`, `1` (read or write failure), `2` (usage error, such as a missing or unknown `-format`).

### When formats increase (future)

If users want to add their own formats, we will move to an interface instead of a `switch` on `Format`. Until demand appears, we keep the current form.

<!-- doccheck: skip: the declaration of report.Writer, quoted for reference -->
```go
type Writer interface {
    Write(w io.Writer, r *Report) error
}
```

### Caveats

- Raw data (JSON Lines) is large, so it is not included in `Report.Write` and has separate APIs (`record.WriteRaw`, `Plan.Raw`).

## 5. Threshold checks (implemented)

Check whether a result meets conditions, so that CI can fail when things get slower (`report/threshold.go`). Close to k6's `thresholds`. Checks are made against the `Report` after the run. `Plan` has no thresholds of its own; to stop a run while it is in progress, use `Plan.AbortOn` (the end of this chapter).

<!-- doccheck: before var rep *report.Report -->
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
| `error_rate` | Share of requests that ended in a transport-level error (of the number of requests). `Expect*` failures are not included | `1%` or `0.01` | `{step=...}` |
| `http_4xx_rate` `http_5xx_rate` | Share of HTTP 4xx / 5xx | Ratio | `{step=...}` |
| `iteration_fail_rate` | Share of failed iterations | Ratio | `{scenario=...}` |
| `iteration_` + `min` `mean` `p50` `p90` `p95` `p99` `max` | Latency of a whole scenario iteration, including Think time (e.g. `iteration_p95`) | Duration | `{scenario=...}` |
| `iteration_active_` + the same statistics | Same, without Think time | Duration | `{scenario=...}` |
| `requests` | Number of requests | Number | `{step=...}` |
| `rps` `dropped` | Requests per second / number of dropped arrivals | Number | Overall only |

#### Names with special characters

An unquoted name in `{...}` runs to the next comma, with the spaces around it trimmed, and braces must be balanced. To select a step or scenario whose name contains a comma, an unbalanced `{` or `}`, `<` or `>` outside braces, or spaces at either end, write the name as a Go double-quoted string:

```
p95{step="list, then order"}<1s
p95{scenario="checkout, mobile",step=" padded "}<1s
p95{step="say \"hi\""}<1s
```

Inside the quotes everything is literal, and `\"` and `\\` are escapes (the other Go escapes such as `\n` and `\u00e9` work too). Unquoted names are read as before, so existing thresholds do not change, with one exception: a value that is a complete quoted string, such as `step="login"`, now means the unquoted name (`login`) instead of a name that includes the quote characters. To select a step whose name really includes quotes, quote it: `step="\"login\""`. A quote that does not form a complete string (`step="abc`, `step="a"b`, or one with an invalid escape such as `step="C:\users"`) is an ordinary character, quotes included. Remember that `\t`, `\n` and similar are escapes, so write a backslash as `\\` inside quotes. An empty name (`step=""`) is an error. `Threshold.String()`, `jotaro check` output, the `AbortOn` messages and the MCP `canonical` text quote a name whenever it needs it, so they can be pasted back.

### Decisions

- When `{step=...}` matches same-named steps in several scenarios, each is checked as a separate result (add `scenario=` to narrow it to one).
- **When the target or data is missing, the threshold counts as not met** (a typo in a step name, zero requests, no `ttfb_` records, etc.), so that a misconfiguration or a test that did not run is not treated as a success.
- `error_rate` is the share of requests that ended in a transport-level error (timeout, connection refused, DNS failure and so on). A failed `Expect*` check is not counted as a request error: it fails the step with `expect_mismatch`, which is listed in the step's errors, and it reaches `iteration_fail_rate` when the scenario returns that error.
- HTTP 4xx/5xx are not in `error_rate` either. View them with `http_4xx_rate` / `http_5xx_rate`.
- To fail a run on wrong responses, return the step's error from the scenario (`return u.Step(...)`, or `return err` after it) and put a threshold on `iteration_fail_rate`, for example `iteration_fail_rate<1%`.
- In the open model, an arrival that comes in while `MaxUsers` is full is dropped, not waited for, and a dropped arrival leaves no latency sample. When you use `corrected_*` thresholds, add `dropped<1` so that a run that dropped arrivals cannot pass on the latencies of the arrivals that did run.

### Stopping a run early (`Plan.AbortOn`) (implemented)

`Plan.AbortOn` is the equivalent of k6's `abortOnFail`: it stops the run as soon as a condition holds, so a broken server is not hit for the whole `Duration` (`abort.go`).

<!-- doccheck: fields jotaro.Plan -->
```go
AbortOn: []jotaro.AbortRule{
	{When: "http_5xx_rate>50%", For: 5 * time.Second},
	{When: "error_rate>50%", For: 5 * time.Second},
	{When: "p95>2s", For: 10 * time.Second},
},
```

- `When` uses the threshold syntax above (`report.ParseThreshold`), but it describes the state to **stop at**, not the state to accept: `http_5xx_rate>50%` aborts once more than half of the requests got a 5xx. A malformed `When` makes `Run` fail before any load is sent.
- It is evaluated every second against the whole run so far (not a sliding window), the same way `Report.Check` reads a finished report. Percentiles are therefore the approximate ones of an incremental aggregation. Nothing counts as holding while there is no data yet.
- `For` is how long the condition must hold without a break (0: at the first evaluation where it holds). A few seconds keep one early failure from stopping a run whose statistics have not settled.
- Mind which metric sees the failure: `error_rate` counts transport-level errors only, so an HTTP error status needs `http_5xx_rate` / `http_4xx_rate`, and wrong responses (`Expect*`) need `iteration_fail_rate`.
- On abort, no new iterations start and the running ones get `Grace`, counted from the abort. `Run` returns the partial report (and finishes `Plan.Raw`) together with an error that wraps `jotaro.ErrAborted`; test for it with `errors.Is(err, jotaro.ErrAborted)`. If `Plan.Progress` is set, a line `aborted: <rule> (at <time>)` is written to it.

### Not supported

- Writing check results into the result file; `-t` for `report` / `merge`.
- Conditions on the time series (per second), and a sliding window for `AbortOn`.

## 6. Related

- Overall design: [`DESIGN.md`](../DESIGN.md) (measurement in chapter 5, open questions in chapter 8)
- Implementation: `record/record.go` / `report/report.go` / `report/aggregate.go` / `report/print.go` / `report/compare.go` / `report/export.go` / `abort.go` / `metrics.go` / `progress.go`
