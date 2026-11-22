# Generator performance

[English](performance.md) | [日本語](performance_jp.md)

How much load one generator (one process running jotaro) can produce, how that is measured, and how it is kept from getting worse. The numbers below are from one machine and one Go version; they show the order of magnitude and the shape, not a promise. Measure on your own machine before you size a fleet.

## What is measured

The benchmarks are in `bench_test.go`. They are not run in CI, because timings on shared runners are noisy:

```sh
go test -run='^$' -bench='Iteration|NetHTTPFloor|RunEndToEnd' -benchmem -count=10 .
```

| Benchmark | What it runs |
| --- | --- |
| `BenchmarkIteration` | One iteration of one `GET` with a query parameter and a bearer header, through `newUser` → `Scenario.run` → `User.Step` → request builder → `executor.do` → the collector and the aggregator. The transport is a stub that returns a canned `200`, so there is no network and no `net/http` transport cost: what is left is the generator's own share plus the cost of `http.Client`. |
| `BenchmarkIterationPostJSON` | The same with a `POST` and a JSON body. |
| `BenchmarkIterationFiveSteps` | Five steps of one request each per iteration, to tell what is paid once per iteration from what is paid per request. |
| `BenchmarkIterationOpenModel` | `BenchmarkIteration` with a scheduled arrival time, as the open model sets for every arrival. |
| `BenchmarkNetHTTPFloor` | The same request sent with `net/http` alone (request, client, `io.ReadAll`) against the same stub. What the benchmarks above spend over this is jotaro's share. |
| `BenchmarkRunEndToEnd` | A whole closed-model `Run` with 16 users for one second against an in-process `httptest` server. It reports `req/s` and `req/s/core` (per `GOMAXPROCS`). |

## Measured ceiling

On an Apple M3 (8 cores), Go 1.27, with other work running on the machine:

| | before #141 | after #141 |
| --- | --- | --- |
| one `GET` iteration (stub transport) | about 3.4 µs, 6.7 KB, 91 allocations | about 3.1 µs, 5.7 KB, 65 allocations |
| one `POST` with JSON | about 4.2 µs, 6.5 KB, 99 allocations | about 3.7 µs, 5.6 KB, 76 allocations |
| five steps in one iteration | about 16.6 µs, 31 KB, 407 allocations | about 14.5 µs, 27 KB, 321 allocations |
| `net/http` alone, same request | about 2–4 µs, 4.0 KB, 38 allocations | (the same) |

So jotaro plus `http.Client` need a few microseconds of CPU and about 65 allocations per request (`net/http` alone accounts for 38 of them, so jotaro's own share is about 27), which is roughly a third of a million requests per second per core before the network. In practice the network, the target and `net/http`'s transport are the limit: `BenchmarkRunEndToEnd`, with the server in the same process (so the server's CPU is included), reached roughly 7 000 to 11 000 requests per second per core on that machine. Plan for a lower figure on a real network, with TLS, or with large bodies, and add generators when the health warning (CPU above 90%) or the `corrected` versus `latency` gap says the generator is the bottleneck, not when a number in this file is reached.

The cost per request on the generator side does not depend on the target. At 20 000 requests per second, jotaro plus `http.Client` now allocate about 1.3 million times per second and 110 MB/s, down from 1.8 million and 135 MB/s.

## What was done, and what was left

The allocation count of the generator's path (jotaro plus `http.Client`) was brought down from 91 to 65 per request (#141):

- the error-kind probes (`classify`) return before they allocate their `errors.As` targets when there is no error, which is every successful step and iteration;
- the request's `httptrace.ClientTrace` lives in the same allocation as its tracer;
- the `User`, its `http.Client` and its cookie jar are one allocation, and the jar itself is created on the first `Set-Cookie` (an iteration that never receives a cookie does not allocate one);
- the `Step` and its executor are one allocation, and the executor hands records to the collector directly instead of through a method value created per step;
- a request without a body passes no body reader to `net/http`, and a response body is read into a buffer sized from `Content-Length` (up to 1 MiB; larger or unknown lengths grow as before).

Left as it was: the `Header.Clone()` of the outgoing request (the cookie jar adds a `Cookie` header to the request it is given, and the builder's header must not change when a request is built once and sent twice), a `http.Client` per iteration (`User.Client` is documented as having its own cookie jar per iteration, and a program may keep it), and the eight hooks of the trace (a hook needs state, so each is a closure; a pool is not safe because `net/http` may still call a hook after the request has returned). Limiting or discarding response bodies is tracked in #100, and the per-arrival setup in the open model in #119.

## Guard

`TestIterationAllocations` (`alloc_test.go`) runs the three scenarios above against the stub and fails when an iteration allocates more than a generous bound. It counts allocations, not time, so it is deterministic and runs in CI; it is skipped under `-race`, which changes the counts, so CI runs it in its own step without `-race` in every test cell. If it fails after a legitimate change, measure with the benchmarks, raise the bound and say why in the pull request.
