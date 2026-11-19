# jotaro threshold syntax

```
metric[{scenario=name,step=name}] operator value
```

Operators: `<` `<=` `>` `>=`. Examples:

```
p95<200ms
error_rate<1%
p99{step=login}<100ms
iteration_fail_rate{scenario=checkout}<=5%
```

| Metric | Meaning | Value |
|---|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | request latency | duration (`200ms`, `1.5s`) |
| the above prefixed with `corrected_` `ttfb_` `dns_` `connect_` `tls_` | after Coordinated Omission correction / TTFB / DNS / TCP / TLS time | duration |
| `error_rate` | share of requests that ended in a transport-level error (timeout, connection refused, DNS failure and so on). A failed `Expect*` check is not counted: see below | `1%` or `0.01` |
| `http_4xx_rate` `http_5xx_rate` | share of HTTP 4xx / 5xx | ratio |
| `iteration_fail_rate` | share of failed iterations (runs of the scenario whose function returned an error) | ratio |
| `iteration_<stat>` / `iteration_active_<stat>` | latency of a whole scenario iteration with / without Think time (`stat` is min, mean, p50, p90, p95, p99 or max; e.g. `iteration_p95{scenario=checkout}<2s`) | duration |
| `requests` `rps` `dropped` | number of requests / requests per second / number of dropped arrivals | number |

- A failed `Expect*` check is not counted as a request error: it fails the step with `expect_mismatch`, which is listed in the step's errors, and it reaches `iteration_fail_rate` when the scenario returns that error. So to fail a run on wrong statuses or bodies, return the step's error from the scenario (`if err := u.Step(...); err != nil { return err }`) and use `iteration_fail_rate` (for example `iteration_fail_rate<1%`); `error_rate` alone would not see it. To watch HTTP error statuses without `Expect*`, use `http_4xx_rate` / `http_5xx_rate`.
- In the open model (`Arrival`), pair `corrected_*` thresholds with `dropped<1`. An arrival dropped because `MaxUsers` was reached never ran, so it is not in the latency samples: `corrected_p95<200ms` can pass while arrivals were being discarded. `dropped<1` makes such a run fail.
- The `{...}` selector narrows the threshold to a scenario and/or a step. Step names must match the names given to `u.Step`.
- A name with a comma, an unbalanced `{` or `}`, `<` or `>` outside braces, or spaces at its ends must be written as a Go double-quoted string: `p95{step="list, then order"}<1s`. Inside the quotes everything is literal, and `\"` and `\\` are escapes. Unquoted names are read as before (up to the next comma, spaces trimmed), except that a value that is a complete quoted string such as `step="login"` now means the unquoted name `login`. The `canonical` text that `validate_threshold` returns quotes a name when it needs to.
- When the target or the data is missing, the threshold counts as **not met**, so a mistyped step name cannot pass silently.
- In Go: `report.ParseThreshold(s)` then `rep.Check(th)`. On the CLI: `jotaro check -t '<threshold>' result.json` (exit code 1 when any threshold is not met). Over MCP: the `check_result` tool.
