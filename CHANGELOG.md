# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow [Semantic Versioning](https://semver.org/) once it reaches 1.0. Until then, we still avoid breaking changes and aim to keep existing code working, in the spirit of the [Go 1 compatibility promise](https://go.dev/doc/go1compat). The exception is fixing an existing bug, which can change behavior that code relied on. Such a change is listed under "Changed" or "Fixed" with a note that it may affect existing code.

## [Unreleased]

### Added

- `Plan.StartUsers`: how many users are already running when the first stage of `Plan.UserStages` begins (default 0), so a run can start at full load instead of ramping up from 0. It can only be set together with `UserStages`.
- `Plan.UserStages` and `UserStage`: change the number of concurrent users over time in the closed model (for example ramp up to 100 users, hold, ramp down), like k6's ramping-vus. It is mutually exclusive with `Plan.Users` and `Plan.Arrival`. Users keep their `User.ID` and iteration count across stops and restarts; a user stopped by a lower target finishes its running iteration first. `Series.Concurrency` shows the resulting load shape. `jotaro mcp`'s lint and authoring guide know the new field.

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
