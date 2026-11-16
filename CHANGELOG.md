# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow [Semantic Versioning](https://semver.org/) once it reaches 1.0. Until then, we still avoid breaking changes and aim to keep existing code working, in the spirit of the [Go 1 compatibility promise](https://go.dev/doc/go1compat). The exception is fixing an existing bug, which can change behavior that code relied on. Such a change is listed under "Changed" or "Fixed" with a note that it may affect existing code.

## [Unreleased]

### Changed

- `Plan.Raw` now streams: each record is written to it as it arrives (`record.RawWriter`) instead of being kept in memory and written at the end, so memory stays constant with `Plan.Raw` too. The `Report` that `Run` returns is aggregated incrementally in that case as well, so its percentiles are within 1% relative error (they were exact before); re-aggregate the raw data with `jotaro report -raw` or `report.FromRaw` for exact ones. Lines are now in arrival order and the stream ends with a second `meta` line holding the final `dropped` and `elapsed_ns`; `ReadRaw` (including older versions) reads it unchanged, and `RawVersion` stays `1`.

### Added

- `jotaro mcp`: the `jotaro://result` resource explains how to save a result and how to read it (structure, what each number and error kind means, what to check first).
- `jotaro mcp`: the `summarize_result` tool returns the key numbers of a saved result file (throughput, error rate, latency per step, dropped arrivals, the worst second) and warnings such as a saturated load generator, without the histograms and the per-second series.
- `jotaro mcp`: the `check_result` tool judges a saved result file (JSON, or raw data with `raw: true`) against thresholds, like `jotaro check`, and returns pass or fail per threshold with the measured value.
- The text report now prints a `warning:` line under `dropped:` when arrivals were dropped by the `Plan.MaxUsers` cap ("I'll tell you exactly what just happened: N arrivals were dropped..."), so drops are hard to miss. Nothing changes when no arrival was dropped.
- `Plan.MetricsAddr`: serve the live state of a run at `/metrics` in the Prometheus text format (requests, responses by status class, errors, throughput, latency quantiles), so Grafana and similar tools can chart it during the run. It is written by hand, adds no dependency, and stops when `Run` returns.
- `jotaro export -format vegeta|csv [-o file] <raw.jsonl | ->`, `report.WriteVegeta` and `Report.WriteSeriesCSV`: convert raw data (the output of `Plan.Raw`) to vegeta's JSON result lines, which `vegeta report -type=json` and `vegeta plot` can read, or to a CSV of per-second request counts, errors, connections, concurrency and latency percentiles. The vegeta format cannot carry scenario or step details, so the attack name is `<scenario>/<step>`.
- Scenario-level latency in thresholds: `iteration_p95`, `iteration_active_p99` and the other `iteration_[active_]<stat>` metrics judge the time of a whole scenario iteration (with or without Think time), for the whole run or per scenario with `{scenario=...}`. The distribution was already in the report as `IterationStats.Total` / `Active`; this makes it usable in `jotaro check`, `AbortOn` and `Plan` thresholds.
- `Plan.Scenarios` and `Weighted`: run a weighted mix of scenarios in one plan (for example 70% browse, 30% checkout). Each iteration (closed model) or arrival (open model) picks one scenario at random in proportion to its weight. It is mutually exclusive with `Plan.Scenario`, and the scenarios must have distinct names. Reports and thresholds already separate results by scenario name. `jotaro mcp`'s lint accepts the new field.
- Richer response checks: `Response.ExpectStatusIn`, `ExpectHeader`, `ExpectBodyContains` and `ExpectJSON(path, want)`, all reported as `expect_mismatch` like `ExpectStatus`, and `Response.JSON(path)` to pull one value out of a JSON body with a dot path such as `data.items.0.id`, without a struct. `ExpectError` gains a `Detail` field for non-status mismatches (`Want` is 0 for them). No new dependencies.
- `jotaro compare base.json new.json` and `report.Compare`: per-step and overall diffs (rps, p50/p95/p99, error rate) between two results, with `-max-p95-regression <percent>` to exit with code 1 on a regression.
- `Plan.AbortOn` and `AbortRule`: stop a run early when a condition holds, for example `{When: "http_5xx_rate>50%", For: 5 * time.Second}`, so a broken server is not hit for the whole `Duration`. `When` uses the threshold syntax (`report.ParseThreshold`) but names the state to stop at. It is evaluated every second against the whole run so far (not a sliding window), and `For` is how long it must hold without a break. On abort no new iterations start and running ones get `Grace` counted from the abort. `Run` returns the partial report (and writes `Plan.Raw`) together with an error wrapping the new `ErrAborted`, and writes an `aborted: ...` line to `Plan.Progress` if it is set.
- `Plan.Progress`: an `io.Writer` that receives one line per second while a run is in progress, such as `12s/60s rps=480 p95=38ms err=0.2%` (elapsed/duration, throughput of the last second, and p95 and error rate over the run so far). It works with and without `Plan.Raw`, adds no recording cost, and writes nothing after `Run` returns.
- `RepeatStages(n, stages...)`: repeats a sequence of `UserStage` n times, so a load shape that goes up and down several times (waves, spikes) does not have to be written out by hand. Join it with other stages using `append`.
- `Plan.StartUsers`: how many users are already running when the first stage of `Plan.UserStages` begins (default 0), so a run can start at full load instead of ramping up from 0. It can only be set together with `UserStages`.
- `Plan.UserStages` and `UserStage`: change the number of concurrent users over time in the closed model (for example ramp up to 100 users, hold, ramp down), like k6's ramping-vus. It is mutually exclusive with `Plan.Users` and `Plan.Arrival`. Users keep their `User.ID` and iteration count across stops and restarts; a user stopped by a lower target finishes its running iteration first. `Series.Concurrency` shows the resulting load shape. `jotaro mcp`'s lint and authoring guide know the new field.

### Fixed

- `Aggregator.Report` returned the aggregator's live `Status` and `Errors` maps, so a report taken while records were still arriving could race with them. It now returns copies, so it is a true snapshot. `Run` itself was not affected: it only built its report after every user had stopped.
- `Series.Concurrency` of a report built from raw data (`FromRaw`, `Plan.Raw`) could still count one more running iteration than there were users, mostly on Windows: the start times read back from JSON have a wall clock reading only, which disagrees a little with the monotonic `Duration`, so back-to-back iterations of one user appeared to overlap. An iteration is now cut at the start of the same user's next iteration, since a user never runs two at once. Iterations of different users are unchanged.
- `Series.Concurrency` could count one more running iteration than there were users when a report was built from records (`Plan.Raw`, `BuildReport`, `FromRaw`): the start offsets were measured on the wall clock and mixed with durations measured on the monotonic clock, so back-to-back iterations of one user could overlap by a few microseconds. Reports built without `Plan.Raw` were not affected.

## [0.1.2] - 2026-11-12

### Changed

- `Run` without `Plan.Raw` now aggregates incrementally: records are folded into histograms as they arrive instead of being kept, so memory no longer grows with the number of requests (5 million requests: about 1165 MB → 9 MB of collector heap in one measurement). As a result, `P50`–`P99` in that report are approximations within 1% relative error; count, min, max and mean stay exact. With `Plan.Raw` set, every record is kept and the percentiles are exact as before. `BuildReport` and `FromRaw` are unchanged.

### Added

- `report.Aggregator`, an incremental alternative to `BuildReport`; `record.Observer` and `record.NewStreamingCollector`, a `Collector` that hands records to an observer instead of keeping them.

## [0.1.1] - 2026-11-02

### Added

- `jotaro version` (also `-version` / `--version`) prints the version. Release binaries report the release tag and commit; `go install ...@vX.Y.Z` reports the module version; other builds report `dev`.

### Fixed

- The GitHub Release is now published with the matching `CHANGELOG.md` section as its notes. The v0.1.0 release was published with an empty body because the GoReleaser configuration skipped the release notes file.

## [0.1.0] - 2026-11-02

First public release.

### Added

- Scenarios written as plain Go code (`Scenario` / `User` / `Step`), with request builders (`Get` / `Post` / ..., headers, query, JSON / form bodies, `Decode`, `ExpectStatus`) and think time.
- Closed model (`Users`) and open model (`Rate` / `Ramp` / `Stages`) load, with Coordinated Omission correction in the open model.
- `Feeder` and `LoadCSV` to hand out per-user data.
- Per-step latency (p50 / p90 / p95 / p99 / max), error breakdown, throughput, TTFB, DNS / TCP / TLS time and connection reuse rate; a per-second time series and the health of the load generator (CPU, goroutines).
- Reports as text, JSON and a self-contained HTML file; raw data as JSON Lines (`Plan.Raw`).
- `jotaro` CLI: `report`, `merge` and `check` (thresholds such as `p95<200ms`, exit code 1 when not met).
- `jotaro mcp`: an MCP server (stdio) that helps write scenarios. It serves the authoring guide and the threshold syntax as resources, a `write_scenario` prompt, and the tools `check_scenario` (syntax, jotaro-specific lint, type check; nothing is executed) and `validate_threshold`.
- `THIRD_PARTY_NOTICES.md` (license texts of the bundled modules) is included in the release archives.
- Runs on Go 1.25 or later; CI on Linux, macOS and Windows.
