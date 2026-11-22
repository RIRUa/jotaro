# Reading a jotaro result

How to save the result of a run, and what its numbers mean. Only what is implemented is described here.

## Saving a result

```go
f, err := os.Create("result.json")
if err != nil {
	panic(err)
}
defer f.Close()
if err := rep.Write(f, report.FormatJSON); err != nil { // FormatText | FormatJSON | FormatHTML
	panic(err)
}
```

- `FormatJSON` is the form the tools and the CLI read back. `FormatHTML` is one self-contained file with charts, for people.
- `Plan.Raw` (an `io.Writer`) additionally writes every record as JSON Lines. Pass `raw: true` to the tools (`-raw` on the CLI) to read that file. `raw: true` is only for that file: an aggregated JSON result is read without it, and each input is rejected with an error when given the wrong way round.
- The JSON holds histograms and a per-second series, so it is large. **Call `summarize_result` instead of reading the file**, and `check_result` to judge it against thresholds.

## Structure

```
Report
├─ open         true for the open model (Arrival), false for the closed model (Users or UserStages)
├─ dropped      arrivals not run because MaxUsers was reached (open model)
├─ elapsed      run time (slightly longer than Plan.Duration)
├─ requests     all requests combined
├─ scenarios[]
│  ├─ iterations  runs of the scenario: count, failed, active (without Think) / total (with Think)
│  └─ steps[]     per u.Step, in the order they first ran: count, failed, errors (why it failed, per kind), requests
├─ series[]     per second: requests, errors, latency, concurrency
└─ health[]     per second: CPU and goroutines of the load generator
```

In the JSON every duration is an integer in nanoseconds (the field name ends in `_ns`). `summarize_result` returns milliseconds.

## What each number means

| Number | Meaning |
|---|---|
| `latency` | time from sending a request until its response has been fully read. Requests that ended in a network error are included |
| `corrected` | time from the **scheduled** start (Coordinated Omission correction). Equal to `latency` in the closed model |
| `ttfb` | time to the first byte of the response. Only requests that received a response |
| `dns` / `connect` / `tls` | only requests in which that phase happened, that is, requests that opened a new connection |
| `status` | responses per class (`2xx`, `4xx`, `5xx`, ...) |
| `errors` | request failures per kind (below), which are transport-level errors only. **HTTP 4xx/5xx are not errors**, and neither is a failed `Expect*` check: that fails the step, not the request |
| error rate | `errors` divided by the number of requests (the `error_rate` threshold metric). It is the share of requests that ended in a transport-level error (timeout, connection refused, DNS failure and so on). A failed `Expect*` check is not counted as a request error: it fails the step with `expect_mismatch`, which is listed in the step's errors, and it reaches `iteration_fail_rate` when the scenario returns that error |
| step `failed` | runs of the step whose function returned an error |
| step `errors` | why those runs failed, per kind (`expect_mismatch` for a failed `Expect*` check, `panic`, or a transport-level kind such as `timeout` that the step returned); the counts add up to `failed`. It is the step's own breakdown, separate from `requests.errors`, which holds only transport-level request errors: a step with `failed` 2, `errors` `{"expect_mismatch": 2}` and empty `requests.errors` failed its checks, not the transport |
| iteration `failed` | runs of the scenario whose function returned an error. A failed step does not fail the iteration when the scenario ignores its error |

| Error kind | Meaning |
|---|---|
| `timeout` | `Plan.Timeout` passed, or the network timed out |
| `connection_refused` | nothing was listening at the address |
| `dns` | the host name could not be resolved |
| `tls` | the handshake or certificate verification failed |
| `canceled` | the run ended while the request was in flight (for example `Plan.Grace` ran out) |
| `panic` | the scenario function panicked: a bug in the test code, not the target. Only steps and iterations have it; the first panic is printed with its stack to `Plan.Progress` or stderr |
| `expect_mismatch` | the response arrived but an `Expect*` check did not match. It is counted in a step's (and, when the scenario returns it, an iteration's) errors and in step `failed`; it does not appear in the request `errors` |
| `other` | anything else |

The `Report` that `Run` returns, and the JSON saved from it, hold p50 to p99 as approximations within 1% relative error, with or without `Plan.Raw`; count, min, max and mean are exact. Exact percentiles come from re-aggregating raw data (`Plan.Raw`) with `jotaro report -raw` or `report.FromRaw`, which `summarize_result` and `check_result` do when given `raw: true`.

## Reading a result

Check these in order. The first two decide whether the other numbers can be trusted at all.

1. **Did the test measure the target?** When the CPU of the load generator reached 90% or more, the generator itself was the bottleneck and latencies look worse than they are. Lower the load or split it over several machines and combine the results with `jotaro merge`.
2. **Was the planned load applied?** `dropped > 0` means `MaxUsers` iterations were already running when new arrivals were due, so fewer scenarios started than planned. The target could not keep up with the rate (or `MaxUsers` is too low for the scenario's length).
3. **Errors.** Look at the kinds, not only the rate. `timeout` and `connection_refused` point at the target being overloaded or down. The request errors say nothing about wrong answers: a step with `failed` above 0 but no request errors, or `4xx` / `5xx` in `status`, means the target answered with something the scenario did not accept (an `Expect*` check failed, `expect_mismatch`). Check `status` for `5xx` (server failure) or `4xx` (the scenario sends something wrong, such as a missing token). `4xx` or `5xx` responses with no failed steps come from requests that are not checked with an `Expect*` call. To make wrong answers fail a threshold, have the scenario return the step's error and judge `iteration_fail_rate`.
4. **Latency per step.** Compare the steps to find the slow one. Prefer p95 and p99 over the mean, and compare `max` with p99 to spot rare stalls.
5. **Open model: `corrected` against `latency`.** `corrected` much larger than `latency` means iterations started later than scheduled, so users would have waited longer than the per-request latency shows. Judge the open model by `corrected_p95` and the like.
6. **Connections.** `dns` / `connect` / `tls` with many observations means connections are not being reused, and their setup time is part of `latency`.
7. **Over time.** The worst second in `summarize_result`, or the charts of the HTML report, show whether the run degraded as it went on (growing latency) or was slow only at one point.

### Warnings of `summarize_result`

`summarize_result` does this arithmetic for you and returns `warnings`, reasons not to trust the numbers, each naming the figures it is based on. Say them to the user before any number. The list is empty when none applies; an empty list is not proof that the result is good. The conditions are deliberately conservative:

| Warning | When it appears |
|---|---|
| no requests | no request was recorded |
| dropped arrivals | `dropped > 0` |
| load generator CPU | the CPU of the load generator reached 90% or more |
| too few requests | the run, or a step, has fewer than 100 requests (p99 is then the slowest request) or fewer than 20 (p95 as well) |
| failures `error_rate` does not show | a step has more failed runs than request errors (a failed `Expect*` check, or an error the scenario returned; the message names the kinds of the step's `errors`), or a scenario has failed iterations while no request failed. Judge `iteration_fail_rate` |
| many 4xx | a step that has failed runs has 10% or more 4xx responses, and at least 10 of them. A step without failed runs is not reported, so a deliberate negative check (`ExpectStatus(404)`, a wrong password answered with `401`) stays quiet; an unchecked step (no `Expect*`) is not covered either. The status classes are all that is kept, so a rate limiter (`429`) cannot be told from a missing token (`401`) or a wrong path (`404`): look at the scenario and at the target's rate limit |
| arrivals started late | open model: `corrected` p95 is at least twice `latency` p95 and at least 10 ms more. `corrected` counts from the scheduled start of the iteration's first request, so time the scenario spends before it (`Think`, other work) shows up as lateness too |
| degraded during the run | the run is 30 s or longer, the load is steady (requests per second in the open model, concurrency in the closed model, within 25% between the first and the last third), and the median per-second p95 of the last third is at least twice the first third's (and 10 ms more), or the share of failed requests (errors and 5xx) rose by 5 percentage points with at least 10 failures in the last third. A deliberate ramp is not reported |

The conditions that are not computed yet (a run cut short, timeouts at the limit, connection churn, a very short run) still have to be read from the numbers as in the list above.

## Limits

- Only HTTP is measured. Requests sent through `u.Client` are not recorded.
- Request latency is kept per step and for the whole run, not per scenario. For a whole scenario, look at the iteration's `active` time.
- There is no statistical comparison of two results. Thresholds judge one result against fixed limits.
