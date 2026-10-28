# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow [Semantic Versioning](https://semver.org/) once it reaches 1.0. Until then, minor versions may contain breaking changes.

## [Unreleased]

First public release (planned as v0.1.0).

### Added

- Scenarios written as plain Go code (`Scenario` / `User` / `Step`), with request builders (`Get` / `Post` / ..., headers, query, JSON / form bodies, `Decode`, `ExpectStatus`) and think time.
- Closed model (`Users`) and open model (`Rate` / `Ramp` / `Stages`) load, with Coordinated Omission correction in the open model.
- `Feeder` and `LoadCSV` to hand out per-user data.
- Per-step latency (p50 / p90 / p95 / p99 / max), error breakdown, throughput, TTFB, DNS / TCP / TLS time and connection reuse rate; a per-second time series and the health of the load generator (CPU, goroutines).
- Reports as text, JSON and a self-contained HTML file; raw data as JSON Lines (`Plan.Raw`).
- `jotaro` CLI: `report`, `merge` and `check` (thresholds such as `p95<200ms`, exit code 1 when not met).
- Runs on Go 1.22 or later; CI on Linux, macOS and Windows.
