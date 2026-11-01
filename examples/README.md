# Examples

[English](README.md) | [日本語](README_jp.md)

Runnable samples of jotaro. Each one starts its own mock server, so nothing else needs to be set up. Run them from the repository root.

| Example | What it shows |
|---|---|
| [`checkout`](checkout) | The basics: a short flow (login → product list → order), closed and open load models, stages, HTML and raw output |
| [`marketplace`](marketplace) | A deep, realistic scenario: several kinds of visitors in one scenario, stateful sessions, retries, polling and thresholds that fail the run |

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
