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
| `error_rate` | share of network errors and `ExpectStatus` mismatches | `1%` or `0.01` |
| `http_4xx_rate` `http_5xx_rate` | share of HTTP 4xx / 5xx | ratio |
| `iteration_fail_rate` | share of failed iterations | ratio |
| `requests` `rps` `dropped` | number of requests / requests per second / number of dropped arrivals | number |

- The `{...}` selector narrows the threshold to a scenario and/or a step. Step names must match the names given to `u.Step`.
- When the target or the data is missing, the threshold counts as **not met**, so a mistyped step name cannot pass silently.
- In Go: `report.ParseThreshold(s)` then `rep.Check(th)`. On the CLI: `jotaro check -t '<threshold>' result.json` (exit code 1 when any threshold is not met).
