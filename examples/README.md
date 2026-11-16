# Examples

[English](README.md) | [日本語](README_jp.md)

Runnable samples of jotaro. Each one starts its own mock server, so nothing else needs to be set up. Run them from the repository root.

| Example | What it shows |
|---|---|
| [`checkout`](checkout) | The basics: a short flow (login → product list → order), closed and open load models, stages, HTML and raw output |
| [`mix`](mix) | A weighted mix of scenarios (70% browse, 30% buy), waves of users, aborting a broken run, live progress and metrics, and thresholds per scenario. A layout to copy for a small load test |
| [`marketplace`](marketplace) | A deep, realistic scenario: several kinds of visitors in one scenario, stateful sessions, retries, polling and thresholds that fail the run |

Which one to read for a flow of your own: when the visitors follow independent flows (browse or buy), write one scenario per flow and mix them with `Plan.Scenarios`, as in `mix`. When one visitor branches on the result of an earlier step, keep one scenario and branch inside it, as in `marketplace`.

## Run, check, read

The whole loop with the `jotaro` command, using `checkout`: run and save the result, judge it against thresholds, then read it. From the repository root, `go run ./cmd/jotaro` is the command. To use it anywhere, install it once with `go install github.com/RIRUa/jotaro/cmd/jotaro@latest` and write `jotaro` instead.

```sh
go run ./examples/checkout -format json -o result.json                             # run and save the result
go run ./cmd/jotaro check -t 'p95<200ms' -t 'error_rate<1%' result.json           # judge it; exit status 1 if a threshold is not met
go run ./cmd/jotaro report -format text result.json                                # read it (HTML by default; add -o result.html)
```

To compare two runs, or to hand the data to other tools, save the raw data as well (`-raw raw.jsonl`), then use `jotaro compare -max-p95-regression 20 base.json new.json` (exit status 1 if a p95 got more than 20% worse) or `jotaro export -format csv -o series.csv raw.jsonl` (also `-format vegeta`).

## checkout

A small sample that starts its own mock server.

```sh
go run ./examples/checkout                                    # 20 fixed users
go run ./examples/checkout -rate 50                           # 50 scenarios per second
go run ./examples/checkout -rate 100 -duration 12s -stages    # ramp up → hold → ramp down
go run ./examples/checkout -format html -o result.html        # HTML report
go run ./examples/checkout -raw raw.jsonl                     # also write raw data (JSON Lines)
```

| Flag | Meaning |
|---|---|
| `-rate N` | Scenarios started per second (open model). `0` (the default) uses the closed model with fixed users |
| `-ramp D` | Time to reach `-rate`. `0` (the default) means constant from the start |
| `-stages` | Build three stages from `-rate`, `-ramp` and `-duration`: ramp up → hold → ramp down |
| `-duration D` | Run time (default `3s`) |
| `-format F` | Result format: `text` (default), `json` or `html` |
| `-o FILE` | Where to write the result; standard output if empty |
| `-raw FILE` | Also write raw data (JSON Lines); not written if empty |

## marketplace

A deeper sample. One scenario plays five kinds of visitors (browsing, member, sign-up, buy-then-cancel, abandoned cart) against a stateful mock shop. It shows:

- expiring sessions with CSRF tokens and automatic re-login
- pagination
- conditional GETs
- retries with backoff and an `Idempotency-Key` on a rate-limited checkout
- polling an asynchronous payment
- thresholds that fail the run

```sh
go run ./examples/marketplace                         # 20 s: ramp up → hold → spike → hold → ramp down
go run ./examples/marketplace -rate 50                # heavy enough to trip the thresholds (exit status 1)
go run ./examples/marketplace -users 10 -pace 0.2     # closed model, think times at 20%
```

| Flag | Meaning |
|---|---|
| `-rate N` | Visitors arriving per second at the steady stages (open model, default `20`) |
| `-users N` | Fixed number of visitors. If above `0`, uses the closed model instead of `-rate` |
| `-duration D` | Run time (default `20s`) |
| `-pace X` | Multiplies every think time; `0.2` is five times faster (default `1`) |
| `-session-ttl D` | Average lifetime of a login session on the mock server (default `4s`) |
| `-newcomers N` | How many new accounts the sign-up visitors can use, each only once (default `40`) |
| `-check` | Judge the result against thresholds and exit with status `1` if one is not met (default `true`) |
| `-format F` / `-o FILE` / `-raw FILE` | Same as in `checkout` |

The tally of what visitors did and what the mock server saw is written to standard error, so that `-format json` can be piped.

## mix

A small load test that puts the plan-level features together. Seventy percent of the iterations run the `browse` scenario and thirty percent run `buy`, against a mock shop. It shows:

- `Plan.Scenarios` with `Weighted`: independent flows picked at random by weight
- `Plan.UserStages`, `RepeatStages` and `StartUsers`: two waves of users that go from 5 up to 20 and back
- `Plan.AbortOn` and `ErrAborted`: stop hitting a broken server, and still print the report of the run up to the abort
- `Plan.Progress` (one line per second on standard error) and `Plan.MetricsAddr` (a Prometheus `/metrics` endpoint, on only with `-metrics`)
- `ExpectStatusIn`, `ExpectJSON` and `Response.JSON(path)` to check and read JSON without declaring a struct
- thresholds per scenario (`{scenario=...}`) and for the time of a whole iteration (`iteration_p95`)

The files are the layout to copy: `main.go` (flags, plan, run, save, check), `scenarios.go`, `thresholds.go` and `mock.go`.

```sh
go run ./examples/mix                           # 12 s, two waves of users
go run ./examples/mix -break-after 2s           # the mock fails after 2 s, so AbortOn stops the run (exit status 1)
go run ./examples/mix -metrics :9090            # also serve http://localhost:9090/metrics while it runs
go run ./examples/mix -format json -o result.json
```

| Flag | Meaning |
|---|---|
| `-duration D` | Run time (default `12s`). The two waves are laid out over it |
| `-break-after D` | Make the mock server answer `503` after this long. `0` (the default) never |
| `-metrics ADDR` | Serve `/metrics` on this address while the run is in progress, such as `:9090`. Off if empty |
| `-check` | Judge the result against the thresholds and exit with status `1` if one is not met (default `true`) |
| `-format F` / `-o FILE` / `-raw FILE` | Same as in `checkout` |

The progress lines go to standard error, so that `-format json` can be piped. Note that `error_rate` counts only requests that ended in a transport-level error. A failed `Expect*` check fails the step instead, and the scenarios return that error so that `iteration_fail_rate` sees it.
