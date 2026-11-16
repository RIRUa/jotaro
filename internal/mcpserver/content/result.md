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
- `Plan.Raw` (an `io.Writer`) additionally writes every record as JSON Lines. Pass `raw: true` to the tools (`-raw` on the CLI) to read that file.
- The JSON holds histograms and a per-second series, so it is large. **Call `summarize_result` instead of reading the file**, and `check_result` to judge it against thresholds.

## Structure

```
Report
├─ open         true for the open model (Arrival), false for the closed model (Users)
├─ dropped      arrivals not run because MaxUsers was reached (open model)
├─ elapsed      run time (slightly longer than Plan.Duration)
├─ requests     all requests combined
├─ scenarios[]
│  ├─ iterations  runs of the scenario: count, failed, active (without Think) / total (with Think)
│  └─ steps[]     per u.Step, in the order they first ran: count, failed, requests
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
| `errors` | failures per kind (below). **HTTP 4xx/5xx are not errors** unless `ExpectStatus` rejected them |
| error rate | `errors` divided by the number of requests (the `error_rate` threshold metric) |
| step `failed` | runs of the step whose function returned an error |
| iteration `failed` | runs of the scenario whose function returned an error. A failed step does not fail the iteration when the scenario ignores its error |

| Error kind | Meaning |
|---|---|
| `timeout` | `Plan.Timeout` passed, or the network timed out |
| `connection_refused` | nothing was listening at the address |
| `dns` | the host name could not be resolved |
| `tls` | the handshake or certificate verification failed |
| `canceled` | the run ended while the request was in flight (for example `Plan.Grace` ran out) |
| `expect_mismatch` | the response arrived but `ExpectStatus` did not match |
| `other` | anything else |

Without `Plan.Raw`, p50 to p99 are approximations within 1% relative error; count, min, max and mean are exact. With `Plan.Raw` (and when reading raw data) the percentiles are exact.

## Reading a result

Check these in order. The first two decide whether the other numbers can be trusted at all.

1. **Did the test measure the target?** When the CPU of the load generator reached 90% or more, the generator itself was the bottleneck and latencies look worse than they are. Lower the load or split it over several machines and combine the results with `jotaro merge`.
2. **Was the planned load applied?** `dropped > 0` means `MaxUsers` iterations were already running when new arrivals were due, so fewer scenarios started than planned. The target could not keep up with the rate (or `MaxUsers` is too low for the scenario's length).
3. **Errors.** Look at the kinds, not only the rate. `timeout` and `connection_refused` point at the target being overloaded or down; `expect_mismatch` means it answered with the wrong status, so check `status` for `5xx` (server failure) or `4xx` (the scenario sends something wrong, such as a missing token). `4xx` or `5xx` responses with no matching errors come from requests that are not checked with `ExpectStatus`.
4. **Latency per step.** Compare the steps to find the slow one. Prefer p95 and p99 over the mean, and compare `max` with p99 to spot rare stalls.
5. **Open model: `corrected` against `latency`.** `corrected` much larger than `latency` means iterations started later than scheduled, so users would have waited longer than the per-request latency shows. Judge the open model by `corrected_p95` and the like.
6. **Connections.** `dns` / `connect` / `tls` with many observations means connections are not being reused, and their setup time is part of `latency`.
7. **Over time.** The worst second in `summarize_result`, or the charts of the HTML report, show whether the run degraded as it went on (growing latency) or was slow only at one point.

## Limits

- Only HTTP is measured. Requests sent through `u.Client` are not recorded.
- Request latency is kept per step and for the whole run, not per scenario. For a whole scenario, look at the iteration's `active` time.
- There is no statistical comparison of two results. Thresholds judge one result against fixed limits.
