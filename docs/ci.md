# Running jotaro in CI

[English](ci.md) | [日本語](ci_jp.md)

A load test that runs on every pull request or every night, fails the build when the service got slower, and leaves behind what you need to see why. This guide builds that on GitHub Actions with a complete workflow; the same steps work in any CI that can run a shell. For running from several machines see [`multi-machine.md`](multi-machine.md), and for how far to trust the numbers see [`tuning.md`](tuning.md).

## What CI needs from jotaro

| Need | What jotaro offers |
|---|---|
| Fail the job when a limit is exceeded | `jotaro check -t ... result.json` exits with 1 when a threshold is not met |
| Fail the job when a run got slower than before | `jotaro compare -max-p95-regression <percent> base.json new.json` exits with 1 when a p95 got worse by more than that |
| Keep the evidence | The JSON result (`jotaro report` turns it into a self-contained HTML page), and optionally raw data (`Plan.Raw`) |
| Show the outcome on the run page | The output of `check` and `compare` is plain text, so it can go to the job summary |

Exit codes, as implemented in `cmd/jotaro/main.go`:

| Command | `0` | `1` | `2` |
|---|---|---|---|
| `jotaro check` | every threshold met | a threshold is not met, or the file cannot be read | usage error (no `-t`, a threshold that cannot be parsed, no file) |
| `jotaro compare` | no gate given, or no p95 regressed beyond the limit | a p95 regressed beyond the limit, or a file cannot be read | usage error |
| `jotaro report` / `merge` | done | a file cannot be read or written, or (`merge`) the results cannot be merged (an open and a closed one) | usage error |

Two details matter in a pipeline. `1` does not tell "the service got slower" from "the file is missing", so check that the file exists before you blame the service. And `jotaro compare` without `-max-p95-regression` only prints the table and always exits with 0.

## The test program

The test is an ordinary Go program in your repository (see [Lay out the project](../README.md#4-lay-out-the-project)). For CI it needs three things: the address of the target from the environment, a result file written even when the run was stopped early, and a non-zero exit code when the run itself failed. The thresholds are left to `jotaro check`, so the program stays the same when you change a limit.

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

var health = jotaro.NewScenario("health", func(ctx context.Context, u *jotaro.User) error {
	return u.Step("get", func(s *jotaro.Step) error {
		return s.Get("/health").Do().ExpectStatus(http.StatusOK)
	})
})

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	rep, runErr := jotaro.Run(ctx, jotaro.Plan{
		Scenario: health,
		BaseURL:  baseURL,
		Arrival:  jotaro.Rate(50, time.Second),
		Duration: 30 * time.Second,
		Progress: os.Stderr, // one line per second in the job log
		AbortOn:  []jotaro.AbortRule{{When: "error_rate>50%", For: 5 * time.Second}},
	})
	if rep == nil {
		return runErr // an invalid Plan: nothing ran
	}

	// Write the result before looking at runErr: an aborted run is the one you want to read.
	if err := os.MkdirAll("results", 0o755); err != nil {
		return err
	}
	f, err := os.Create("results/result.json")
	if err != nil {
		return err
	}
	if err := rep.Write(f, report.FormatJSON); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return runErr // wraps jotaro.ErrAborted when AbortOn stopped the run
}
```

Notes on the choices:

- `jotaro.Run` returns the partial report **and** an error when `AbortOn` stops the run, so the file is written first and the error decides the exit code afterwards. The job fails and the artifact still has the numbers up to the abort.
- The open model (`Arrival`) keeps its pace when the service slows down, which is what a regression test wants. Pair it with a `dropped<1` threshold below, because a run that dropped arrivals only measured the ones that ran.
- The run lasts long enough to hold a few hundred requests at least. With fewer than 100 requests p99 is just the slowest request, and with fewer than 20 so is p95 (the same limits `summarize_result` warns about).

## The workflow

This is a complete `.github/workflows/load-test.yml`. It starts the service with `docker compose`, waits until it answers, runs the test, gates on `jotaro check`, compares with the last successful run on `main`, and keeps the results as an artifact whatever happens.

```yaml
name: load-test

on:
  pull_request:
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read
  actions: read # download the baseline artifact of an earlier run

concurrency:
  group: load-test-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

jobs:
  load-test:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    env:
      JOTARO_VERSION: v0.1.3
      BASE_URL: http://localhost:8080
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0
        with:
          go-version-file: go.mod

      - name: Install the jotaro CLI
        run: |
          go install "github.com/RIRUa/jotaro/cmd/jotaro@${JOTARO_VERSION}"
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
          "$(go env GOPATH)/bin/jotaro" version

      - name: Start the service and wait until it answers
        run: |
          docker compose up -d --build
          curl --fail --silent --show-error --retry 30 --retry-delay 2 --retry-all-errors "$BASE_URL/health"

      - name: Run the load test
        run: go run ./loadtest

      - name: Check the thresholds
        run: |
          status=0
          jotaro check \
            -t 'p95<300ms' -t 'error_rate<1%' -t 'http_5xx_rate<1%' \
            -t 'iteration_fail_rate<1%' -t 'dropped<1' \
            results/result.json > results/check.txt 2>&1 || status=$?
          cat results/check.txt
          { echo '### jotaro check'; echo '```'; cat results/check.txt; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
          exit "$status"

      - name: Download the baseline (the last successful run on main)
        if: always()
        env:
          GH_TOKEN: ${{ github.token }}
          GH_REPO: ${{ github.repository }}
        run: |
          id=$(gh run list --workflow load-test.yml --branch main --event push --status success --limit 1 --json databaseId --jq '.[0].databaseId')
          if [ -n "$id" ] && [ "$id" != "null" ]; then
            gh run download "$id" --name load-test-results --dir baseline || echo "the baseline artifact is gone (expired?)"
          else
            echo "no successful run on main yet"
          fi

      - name: Compare with the baseline
        if: always() && hashFiles('baseline/result.json') != '' && hashFiles('results/result.json') != ''
        continue-on-error: true # shared runners are noisy: report a regression, do not block (see below)
        run: |
          status=0
          jotaro compare -max-p95-regression 30 baseline/result.json results/result.json > results/compare.txt 2>&1 || status=$?
          cat results/compare.txt
          { echo '### jotaro compare (against the last successful run on main)'; echo '```'; cat results/compare.txt; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
          exit "$status"

      - name: Build the reports
        if: always() && hashFiles('results/result.json') != ''
        run: |
          jotaro report -o results/report.html results/result.json
          jotaro report -format text -o results/report.txt results/result.json
          if grep -q '^warning:' results/report.txt; then
            echo "::warning title=jotaro::$(grep -m 1 '^warning:' results/report.txt)"
          fi
          { echo '<details><summary>jotaro report</summary>'; echo; echo '```'; cat results/report.txt; echo '```'; echo; echo '</details>'; } >> "$GITHUB_STEP_SUMMARY"

      - name: Upload the results
        if: always()
        uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: load-test-results
          path: results/
          if-no-files-found: ignore
          retention-days: 30

      - name: Show the logs of the service
        if: failure()
        run: docker compose logs --no-color --tail 200

      - name: Stop the service
        if: always()
        run: docker compose down
```

The service comes from a `compose.yaml` in the repository, for example:

```yaml
services:
  app:
    build: .
    ports:
      - "8080:8080"
```

### What each step does

- **Pinned versions.** `go install ...@v0.1.3` installs exactly that release of the CLI (the module path is `github.com/RIRUa/jotaro`, and `jotaro version` prints the version into the log). Keep it equal to the version in your `go.mod`, so that the library that wrote the result and the CLI that reads it are the same release. The actions are pinned to a commit SHA with the version in a comment, as in this repository's own workflows; Dependabot can bump the actions.
- **Readiness.** `docker compose up -d` returns when the containers have started, not when the service answers. The `curl --retry` loop waits for a successful `/health` (up to about a minute), so the test does not start against a service that is still booting, and a service that never comes up fails here instead of producing a run full of `connection_refused`. Instead of the loop, a service container with a health check works as well: the job does not start its steps until the container is healthy.
- **`if: always()`.** The interesting runs are the failing ones. Every step that collects evidence has `always()`, so a failed threshold still produces the baseline comparison, the HTML report and the artifact. The steps that read `results/result.json` first test that it exists, which covers a run that died before writing it.
- **The gate.** `jotaro check` reads the result and exits with 1 if any threshold is not met. The output goes to a file first, so the exit status survives and the same text reaches both the log and the job summary (`$GITHUB_STEP_SUMMARY`). A threshold whose data is missing (a typo in a step name, a run without requests) counts as **not met**, so a misconfigured gate fails instead of passing silently. The syntax is in [`result.md`](result.md#5-threshold-checks-implemented) and the README.
- **Warnings are not thresholds.** The load generator's CPU reaching 90% is shown as a `warning:` line in the text report, not as something `jotaro check` can judge. The "Build the reports" step turns it into a workflow annotation and puts the whole text report into the summary, so the line is not buried. Read it before you trust a pass (see [`tuning.md`](tuning.md)).
- **The artifact.** `report.html` is one self-contained file: download the artifact and open it in a browser. `result.json` is what `jotaro compare` and `jotaro merge` read later. The JSON grows with the length of the run (a 2-second run of the `checkout` example is 59 KB), so keep `retention-days` and the run length in proportion; a long run is better kept for a few days than for the default 90.

## Comparing with a baseline

`jotaro compare` prints rps, p50, p95, p99 and the error rate overall and per step, and with `-max-p95-regression <percent>` it exits with 1 when any p95 (overall or of a step) got worse by more than that. Only the p95 is gated. There is no statistical test of whether a difference is significant.

**Where to keep the baseline.**

- **The artifact of the last successful `push` run on `main`**, as in the workflow. The lookup asks for `--event push`, because `--branch main` alone also matches a run of a pull request from a fork whose branch happens to be called `main`, and an untrusted run must never become the baseline. Nothing to maintain: every merge moves the baseline, and a `main` run that failed its thresholds is never a baseline because only successful runs are listed. Artifacts expire (`retention-days`), and the step above handles a baseline that is not there. The first run has none, so nothing is compared.
- **A committed file** such as `loadtest/baseline.json`. The baseline changes only when someone decides it should, and a pull request shows the change. The cost is that it ages: an intended slowdown has to be committed as the new baseline, and the file is large (see the size note above). Replace the download step with `cp loadtest/baseline.json baseline/result.json`, or point `compare` at the committed file directly.

**Shared runners are noisy.** A GitHub-hosted runner shares its host with other jobs, so the same commit gives different latencies from run to run, and your service and the load generator share the runner's few CPUs (see [The generator and the target on the same machine](tuning.md#the-generator-and-the-target-on-the-same-machine)). A relative gate such as 10% on a p95 will fail on noise. What to do:

- Measure the noise before choosing a limit: start the workflow several times on the same commit (`workflow_dispatch`) and look at how far the p95 moves. Set the limit well above that spread. The `30` in the workflow is a placeholder, not a recommendation derived from measurements.
- Start with the comparison as information only (`continue-on-error: true`, as above) and keep absolute thresholds as the gate. Remove `continue-on-error` once the noise is known and the limit is safe.
- Compare what is stable: the p95 of a step over many requests is steadier than a p99 or a max. `jotaro compare` gates the p95 only, and reads the others for you to judge.
- For a hard gate, run on a dedicated runner (a self-hosted machine or a larger runner with a fixed size) and keep the target on another host.

## Variations

- **A service container** instead of `docker compose`: put the service under `jobs.<id>.services` with a `--health-cmd` in `options`, and the job waits until it is healthy before it starts the steps. The service is then reachable on `localhost` at the published port.
- **The release archive** instead of `go install`, which saves the compile time of the CLI. Download the archive and `checksums.txt` of a release, check the sum and unpack it:

  ```sh
  base=https://github.com/RIRUa/jotaro/releases/download/v0.1.3
  curl -fsSL -O "$base/jotaro_0.1.3_linux_amd64.tar.gz" -O "$base/checksums.txt"
  sha256sum --check --ignore-missing checksums.txt
  tar -xzf jotaro_0.1.3_linux_amd64.tar.gz jotaro
  ```

  The archive names are `jotaro_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows). The release workflow also attaches build provenance attestations to the archives (since [#167](https://github.com/RIRUa/jotaro/pull/167); earlier releases have none).
- **Another CI.** Nothing above is specific to GitHub apart from the YAML: install the CLI, start the service, run the program, run `jotaro check`, keep `results/`. A step fails by its exit code, and the text output of `check` and `compare` can go to whatever summary the CI has.
- **A load that does not fit on one machine**: run the program on several machines and merge the results; see [`multi-machine.md`](multi-machine.md).

## Known limits

- `jotaro check` has no thresholds for the health of the load generator, so a saturated generator is visible as a warning in the report and not as a failure of the gate. Read the warnings before you trust a pass, see [`tuning.md`](tuning.md).
- Exit code 1 of `check` and `compare` also means an unreadable file, as noted above; guard on the file existing, as the workflow does.
- `check` has no machine-readable output and thresholds cannot be read from a file yet ([#97](https://github.com/RIRUa/jotaro/issues/97)), so they are written on the command line. `compare` gates the p95 only, and does not show steps that exist in one result ([#98](https://github.com/RIRUa/jotaro/issues/98)).
- A composite action or a reusable workflow for this is not provided. The workflow above is meant to be copied.
