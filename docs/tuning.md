# Tuning the load generator and trusting the result

[English](tuning.md) | [日本語](tuning_jp.md)

A load test measures the target only when the machine that generates the load is not the bottleneck and the numbers mean what you think they mean. This guide covers the limits of the generator's machine that people hit first, what a few numbers in the result do and do not say, and a checklist to run before you believe a result. Numbers quoted here come from runs on one laptop (8 cores, macOS) while this guide was written; they show the shape of a symptom and are **machine-dependent**, not benchmarks.

For the meaning of every field of a result see [`result.md`](result.md). For running in CI see [`ci.md`](ci.md), and for splitting a load over several machines see [`multi-machine.md`](multi-machine.md).

## Limits of the generator's machine

The first three things that go wrong on the generator are file descriptors, ephemeral ports and CPU. The first two show up as errors, which today land in `other` ([#110](https://github.com/RIRUa/jotaro/issues/110) asks for them to be classified). A run that hits them says nothing about the target.

### File descriptors

Every connection that is open at the same time is a file descriptor on the generator, and over HTTP/1.1 every request that is in flight needs a connection of its own. In the open model the number of iterations that run at once is capped by `Plan.MaxUsers`, which is **1 000 by default** (0 means 1 000), and each iteration may have a request in flight (more with `Step.Parallel`). Concurrency is about the rate times the time one iteration takes (3 000 arrivals per second of 400 ms each is 1 200 at once, so the default cap would drop arrivals first). The closed model has about as many connections as `Users`. A few more descriptors go to the process itself (standard streams, the file of `Plan.Raw`, the `MetricsAddr` listener).

What matters is the **hard** limit. Go programs raise the soft limit of open files to the hard limit (minus one; on macOS at most `kern.maxfilesperproc`) when they start on Unix (since Go 1.19; jotaro needs Go 1.25), so the usual soft limit of 1 024 does not bite by itself. Check what the process can really have:

```sh
ulimit -Hn          # the hard limit: this is the ceiling of a Go program
ulimit -n 65536     # raise it for this shell and its children (raising the hard limit needs privileges)
```

Where the limit comes from decides how to raise it: `ulimit` in the shell that starts the test, `LimitNOFILE=` in a systemd unit, `--ulimit nofile=65536:65536` for `docker run`, the settings of the container runtime in Kubernetes. A container or a CI image is the usual place for a hard limit that is low.

**Symptom.** The run reports `errors: other=N` with a large N, next to few or no 2xx. Reproduced here by limiting the process (`ulimit -Sn 128; ulimit -Hn 128`) and running 200 users for 4 seconds against a local server:

```
requests:   934323 (232409.79/s)
status:     2xx=24032
errors:     other=910291
conns:      new=124 reused=23908 (99.5% reused)
```

97% of the "requests" failed at once because no socket could be created, and a closed-model user whose request fails immediately sends the next one at full speed ([#99](https://github.com/RIRUa/jotaro/issues/99)), which is why there are so many. Today `other` cannot tell this from a reset by the target (it is the catch-all kind), so look at the setup: `ulimit -Hn` against the concurrency you planned, and the number of descriptors the process holds during the run (`ls /proc/<pid>/fd | wc -l` on Linux, `lsof -p <pid> | wc -l` elsewhere).

### Ephemeral ports and TIME_WAIT

Each connection to one address and port uses a local (ephemeral) port, and a closed connection keeps it in `TIME_WAIT` for a while (60 seconds on Linux), on the side that closed it first. With the Linux defaults, the range `net.ipv4.ip_local_port_range` is 32768 to 60999, about 28 000 ports, so a generator that opens **new connections** to a single address faster than roughly 28 000 / 60 s, about 470 per second, runs out of ports. The default range can differ on your system; read it with `sysctl net.ipv4.ip_local_port_range`, and count the sockets in `TIME_WAIT` with `ss -s` (Linux) or `netstat -an | grep -c TIME_WAIT`.

In a normal run this does not happen, because jotaro reuses connections: all users share one `http.Transport` and every response body is read to the end, which keeps the connection reusable. In the runs for this guide the reuse rate was 99% or more (200 users, `conns: new=200 reused=38720 (99.5% reused)`). It happens when reuse breaks:

- the target or a proxy in front of it closes the connection after each response (`Connection: close`, HTTP/1.0, a very short keep-alive timeout). The `TIME_WAIT` entries then mostly sit on the closing side, the target, but every request still dials a new connection with its handshake;
- bursts that need more connections than the transport keeps idle (the default transport keeps at most 100 idle connections, and 2 per host) leave the extra ones to be closed by the generator, which is the case in which the generator itself holds the `TIME_WAIT` entries and runs out of ports.

**Symptom.** The `conns:` line of the report shows a low reuse rate, and the `connect:` (and `dns:`, `tls:`) lines have about as many observations as there are requests, because each request dialed. The error that ends the run is `connect: cannot assign requested address`, today counted as `other`. Fix the cause: keep-alive on the target side, or spread the load over more target addresses, or give the generator a wider port range, or use several generators (each has its own ports; see [`multi-machine.md`](multi-machine.md)).

### CPU, GOMAXPROCS and containers

The report has a `loadgen:` line (for example `cpu max 5.2% (avg 5.1%), goroutines max 602`), and when the maximum CPU of any second reaches **90%** the text and HTML reports print a warning: the generator itself may be the bottleneck, and the measured latencies look worse than the target's real behavior. The percentage is the CPU time the process used in that second, divided by the time and by `GOMAXPROCS`, so 100% means all the CPUs the Go runtime was allowed to use. Where it cannot be measured (Windows today, [#95](https://github.com/RIRUa/jotaro/issues/95)) it is -1 and `cpu n/a`. `jotaro check` cannot judge it, so read the warning (the workflow in [`ci.md`](ci.md) turns it into an annotation).

- **How much does it cost?** Little for simple requests: a run with 200 users against a local server that answers in 20 ms sent about 9 700 requests per second at `cpu max 5.2%` of 8 cores, about 0.4 of a core (the server was another process). Decoding JSON, large bodies (every response is read fully into memory, [#100](https://github.com/RIRUa/jotaro/issues/100)) and scenario code cost more, so measure your own scenario: raise the load in steps and watch `loadgen:`. [`performance.md`](performance.md) has the generator's own cost per request and how it is benchmarked.
- **`GOMAXPROCS` and containers.** The CPU percentage is relative to `GOMAXPROCS`, so that number must be what the machine can really give you. With Go 1.25 and later the runtime sets `GOMAXPROCS` from the CPU limit of the container (cgroup) as well, and keeps it up to date, when the `go` line of your `go.mod` is 1.25 or later (jotaro requires 1.25, so yours is). Older runtimes use the number of cores of the host: in a container limited to 2 CPUs on a 64-core host, such a process runs 64 threads against a 2-CPU quota, is throttled, and reads a CPU of a few percent while it is saturated. Setting `GOMAXPROCS` yourself (an environment variable) overrides it; set it to the CPU limit if the runtime does not see it.
- **When it is hot**: lower the rate or the users, give the generator more cores, or split the load over several generators ([`multi-machine.md`](multi-machine.md)). Do not trust latencies from a run that raised the warning.

### The generator and the target on the same machine

The examples start their own mock server in the same process so that they run with nothing else to set up. A real test should not do that, and a run on one machine is a smoke test, not a capacity measurement:

- they share the CPUs, so the target and the generator slow each other down, and the CPU figure of the generator includes the target when it runs in the same process (as in `examples/checkout`);
- the traffic goes over the loopback interface, which has no network latency, no packet loss and no bandwidth limit, so the latencies are lower than a client elsewhere would see and a saturated network is never part of the result;
- the sockets, the memory and the `TIME_WAIT` entries of both sides come from one kernel, so the limits above are reached sooner.

Put the generator on its own machine close to the target in the network, or at the distance you want to model. A CI job that runs both on one runner is a reasonable regression test if you compare runs of the same setup with each other (see [`ci.md`](ci.md)), and nothing more.

## What the numbers mean

### `corrected` and `latency`

`latency` is the time from sending a request until its response has been read. `corrected` is the time from the **scheduled** start, and the two are equal in the closed model. In the open model (`Arrival`) only the first request of an iteration takes the scheduled arrival time; later requests of the iteration have `corrected` equal to `latency`. So the gap between them is the time that passed between the scheduled arrival and the moment the first request was sent.

In the current implementation arrivals are never queued: when `MaxUsers` iterations are already running the arrival is dropped and counted (`dropped`), not delayed. A target that cannot keep up therefore shows as **`dropped`** and as growing concurrency, and the `corrected` of the iterations that did run stays close to `latency`. Reproduced here with a 20 ms target, 2 000 arrivals per second and `MaxUsers: 20`: `latency` p50 20.0 ms, `corrected` p50 20.4 ms, `dropped` 4 252 (about half of the arrivals). What does put a gap between the two is on the generator's side or in the scenario: the timer that wakes the dispatcher, starting the goroutine, creating the user (a cookie jar and an HTTP client per arrival), a saturated CPU or a garbage collection, and any work or `Think` the scenario does before its first request.

**The generator's own floor.** Even with nothing queued anywhere, `corrected` is above `latency` by a constant that the generator adds. [#119](https://github.com/RIRUa/jotaro/issues/119) measured 0.5 to 1 ms on an idle 4-core Linux machine at 100 to 5 000 arrivals per second (at 100 per second: `latency` p50 266 µs, `corrected` p50 918 µs). On the laptop used for this guide, a 100 per second run against a 20 ms target with no drops gave `latency` p50 20.8 ms and `corrected` p50 22.09 ms: 1.3 ms more. The likely cause is the granularity of the dispatcher's timer, and the fix (measure it as a `schedule_delay` of its own, wake up early) is still open. Until then:

- do not set `corrected_*` thresholds that a fast target cannot meet because of this floor (`corrected_p95<1ms` cannot be met by any target);
- do not read a difference of about a millisecond between `corrected` and `latency` as a property of the target. A gap of tens of milliseconds, or a `dropped` above zero, is a signal.

### HTTP/2 connection sharing

All virtual users share one `http.Transport`. Over HTTP/1.1 that means about one connection per request in flight. Over HTTPS to a target that offers HTTP/2, Go negotiates HTTP/2 (the cloned default transport has `ForceAttemptHTTP2` on) and **multiplexes all users over one connection**. Measured here with 50 users for 2 seconds against a local TLS server:

| | HTTP/1.1 | HTTP/2 |
|---|---|---|
| `conns:` in the report | `new=50 reused=4728` | `new=1 reused=4754` |
| connections the server accepted | 50 | 49 to 50, almost all of them start-up dials that were closed unused |

What it means for a test ([#112](https://github.com/RIRUa/jotaro/issues/112)): 1 000 "users" are one client. A load balancer that pins a connection to a backend sends all of them to one instance; one lost packet stalls every stream at once; per-connection limits on the server (`MAX_CONCURRENT_STREAMS`, rate limits per connection) throttle the whole test. `conn_new` and the TLS timings describe one handshake, not the connection cost of a user. The server may also accept many more connections than `conn_new`, because the extra connections that `net/http` dials at start-up and closes unused are not counted ([#184](https://github.com/RIRUa/jotaro/issues/184)).

The result does not record the protocol, so state it next to the numbers. You can tell from the `conns:` line (`new` far below the users, with HTTPS) and from the server. To force one connection per concurrent request again, turn HTTP/2 off on `http.DefaultTransport` before `Run`; `Run` clones it. This changes the process-wide transport, which is fine for a program that only runs the test, and it relies on that cloning (`Plan.Transport` is the proper place for it, [#89](https://github.com/RIRUa/jotaro/issues/89)):

```go
package main

import (
	"crypto/tls"
	"net/http"
)

// forceHTTP1 turns HTTP/2 off for the runs of this process. Call it before jotaro.Run.
func forceHTTP1() {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return // a replaced default transport: nothing to change
	}
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
}

func main() { forceHTTP1() }
```

With it, the same run as above showed `new=50 reused=4600` and the server saw no HTTP/2 request.

### `bytes_in` and `bytes_out`

`bytes_in` is the length of the response body **after** `net/http` decompressed it: the transport asks for gzip by itself and removes the encoding before the scenario sees the body. `bytes_out` is the length of the request body; the request line and headers are not counted, and neither are response headers. They are not wire traffic ([#114](https://github.com/RIRUa/jotaro/issues/114)). Measured here: a handler that returns 100 000 bytes of text gzip-encoded (183 bytes on the wire) gave 2 461 requests and `bytes: in 234.7MB`, which is 2 461 × 100 000 bytes (234.7 MiB), and `out 0B` for requests that carried a bearer token of 228 characters in a header.

Use them to compare the size of what the scenario handled between runs, not to judge a link or an egress quota. For bandwidth, read the counters of the network interface (or your monitoring) on the generator and the target.

## Before believing a result

The list below is the one the MCP resource `jotaro://result` serves under "Reading a result" ([`internal/mcpserver/content/result.md`](../internal/mcpserver/content/result.md)); the wording is the same, so an assistant and a person read the same thing. Items 8 to 12 are added here for the generator's side. Check them in order: the first two decide whether the other numbers can be trusted at all.

<!-- Items 1 to 7 are copied from internal/mcpserver/content/result.md ("Reading a result"). Keep them the same. -->

1. **Did the test measure the target?** When the CPU of the load generator reached 90% or more, the generator itself was the bottleneck and latencies look worse than they are. Lower the load or split it over several machines and combine the results with `jotaro merge`.
2. **Was the planned load applied?** `dropped > 0` means `MaxUsers` iterations were already running when new arrivals were due, so fewer scenarios started than planned. The target could not keep up with the rate (or `MaxUsers` is too low for the scenario's length).
3. **Errors.** Look at the kinds, not only the rate. `timeout` and `connection_refused` point at the target being overloaded or down. The request errors say nothing about wrong answers: a step with `failed` above 0 but no request errors, or `4xx` / `5xx` in `status`, means the target answered with something the scenario did not accept (an `Expect*` check failed, `expect_mismatch`). Check `status` for `5xx` (server failure) or `4xx` (the scenario sends something wrong, such as a missing token). `4xx` or `5xx` responses with no failed steps come from requests that are not checked with an `Expect*` call. To make wrong answers fail a threshold, have the scenario return the step's error and judge `iteration_fail_rate`.
4. **Latency per step.** Compare the steps to find the slow one. Prefer p95 and p99 over the mean, and compare `max` with p99 to spot rare stalls.
5. **Open model: `corrected` against `latency`.** `corrected` much larger than `latency` means iterations started later than scheduled, so users would have waited longer than the per-request latency shows. Judge the open model by `corrected_p95` and the like.
6. **Connections.** `dns` / `connect` / `tls` with many observations means connections are not being reused, and their setup time is part of `latency`.
7. **Over time.** The worst second in `summarize_result`, or the charts of the HTML report, show whether the run degraded as it went on (growing latency) or was slow only at one point.
8. **Errors of the generator's setup.** Many `other` errors with few responses is a limit of the generator, not of the target: the file descriptor limit or the ephemeral ports (see [above](#limits-of-the-generators-machine)). Until [#110](https://github.com/RIRUa/jotaro/issues/110) classifies them, `other` cannot say which side failed.
9. **The floor of `corrected`.** Read item 5 with [`corrected` and `latency`](#corrected-and-latency): in this implementation an arrival that finds `MaxUsers` full is dropped, so a slow target shows as `dropped` (item 2), and a gap of about a millisecond is the generator's own.
10. **HTTPS: one connection or many?** `conn_new` far below the number of users, or a single TLS handshake, means HTTP/2 carried all users over one connection ([HTTP/2 connection sharing](#http2-connection-sharing)). The result does not say which protocol ran.
11. **Where did the generator run?** Next to the target on the same machine or runner, the CPU, the network and the sockets are shared and the traffic is loopback ([the same machine](#the-generator-and-the-target-on-the-same-machine)). Say so next to the numbers.
12. **Enough requests?** With fewer than 100 requests p99 is just the slowest request, and with fewer than 20 so is p95. `summarize_result` warns about this; with `jotaro check` and the CLI, count `requests` yourself.

The MCP tool `summarize_result` does part of this arithmetic and returns `warnings`; see "Warnings of `summarize_result`" in the same resource. It does not cover items 8 to 11.
