# Running from several machines

[English](multi-machine.md) | [日本語](multi-machine_jp.md)

When one load generator cannot produce the load you need, run the same test on several machines at the same time and combine the results with `jotaro merge` (or `report.MergeReports`) into one result that reads like a single run. jotaro does not coordinate the machines: it has no controller and no agents. You split the load, start the machines together and collect the files; this guide shows how, and what the merged numbers mean.

## When to use several generators

Not before one machine has been tuned and found to be the limit; see [`tuning.md`](tuning.md). The signs are:

- the report warns that the CPU of the load generator reached 90% (the `loadgen:` line and a `warning:` line of the text report, and the same in the HTML report);
- the generator hit a limit of the machine that more tuning cannot lift, such as the network interface or the number of connections it can open to one address ([`tuning.md`](tuning.md#limits-of-the-generators-machine));
- the load should come from several places on purpose.

On a laptop-class machine one generator goes a long way: [the CPU section of `tuning.md`](tuning.md#cpu-gomaxprocs-and-containers) has a measured example. Several generators also cost you something: more to start and collect, clocks to keep in step, and a result to merge.

## Splitting the load

Every machine runs the same program with its own share, and the shares add up to the load you want.

- **Closed model (`Users`)**: divide the users. When the number does not divide evenly, give the first few machines one more, so that the total stays exact (`share` in the program below).
- **Open model (`Arrival`)**: divide the rate. `Rate(n, per)` takes whole numbers, so `Rate(1000/3, time.Second)` is 333 per second on each machine and 999 in total. Use `Rate(1000, 3*time.Second)` instead: 1 000 arrivals per 3 seconds is the exact third of 1 000 per second. `MaxUsers` is a cap **per generator** (0 means 1 000 each), so divide it too if the total cap matters.
- **Stages** (`UserStages`, `Stages`, `Ramp`): divide every target or rate in the same way.
- **Feeder rows**: each process has its own `Feeder` that starts at the first row, so on N machines the first user of every machine gets the first row. Data that must not be shared (accounts that can be used once, `Once`; sessions that cannot be open twice) has to be split by hand: give every machine its own part of the rows. The program below takes every N-th row. Make sure each part lasts the run: with `Once`, `Next` returns false when the rows are used up.
- **User IDs**: `User.ID` counts from 0 on every machine (in the open model each arrival has its own ID, again from 0 on every machine). A scenario that picks an account or a number from `u.ID` would use the same value on each machine; add an offset per machine or use a `Feeder`.
- **Random feeders**: a `Random` feeder picks by itself on each machine, so the machines overlap in what they pick (and there is no seed to make it repeat, [#124](https://github.com/RIRUa/jotaro/issues/124)).

## A program that takes its share

The program is the one you would run on one machine, with flags for its number, the number of machines and the time to start. It writes its own result file.

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

// share is the part of total that machine number shard (0-based) of shards runs. The shares add up to total.
func share(total, shard, shards int) int {
	n := total / shards
	if shard < total%shards {
		n++
	}
	return n
}

// shardRows returns every shards-th row from row shard on, so that no two machines get the same row.
func shardRows[T any](rows []T, shard, shards int) []T {
	var mine []T
	for i := shard; i < len(rows); i += shards {
		mine = append(mine, rows[i])
	}
	return mine
}

func newScenario(accounts *jotaro.Feeder[map[string]string]) *jotaro.Scenario {
	return jotaro.NewScenario("login", func(ctx context.Context, u *jotaro.User) error {
		acc, ok := accounts.Next()
		if !ok {
			return fmt.Errorf("no account left for user %d", u.ID)
		}
		return u.Step("login", func(s *jotaro.Step) error {
			return s.Post("/login").JSON(map[string]string{"user": acc["user"]}).Do().ExpectStatus(http.StatusOK)
		})
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func run() error {
	shard := flag.Int("shard", 0, "number of this machine, from 0 to shards-1")
	shards := flag.Int("shards", 1, "number of load generators")
	startAt := flag.Int64("start-at", 0, "Unix time in seconds at which every machine starts; 0 starts at once")
	users := flag.Int("users", 100, "virtual users in total, over all machines")
	duration := flag.Duration("duration", time.Minute, "run time")
	out := flag.String("o", "result.json", "where to write the result")
	flag.Parse()
	if *shards < 1 || *shard < 0 || *shard >= *shards {
		return fmt.Errorf("-shard must be from 0 to -shards-1, got %d of %d", *shard, *shards)
	}

	f, err := os.Open("testdata/accounts.csv")
	if err != nil {
		return err
	}
	rows, err := jotaro.LoadCSV(f)
	f.Close()
	if err != nil {
		return err
	}
	accounts := jotaro.NewFeeder(shardRows(rows, *shard, *shards), jotaro.Circular)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *startAt > 0 { // wait for the common start time, so that all machines overlap
		select {
		case <-time.After(time.Until(time.Unix(*startAt, 0))):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	rep, runErr := jotaro.Run(ctx, jotaro.Plan{
		Scenario: newScenario(accounts),
		BaseURL:  os.Getenv("BASE_URL"),
		Users:    share(*users, *shard, *shards),
		Duration: *duration,
		Progress: os.Stderr,
	})
	if rep == nil {
		return runErr // an invalid Plan (for example fewer users than machines): nothing ran
	}
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := rep.Write(file, report.FormatJSON); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return runErr
}
```

For the open model, replace the `Users` line of the plan with the exact third of the rate, and divide the cap:

<!-- doccheck: before var shards int -->
<!-- doccheck: fields jotaro.Plan -->
```go
Arrival:  jotaro.Rate(1000, time.Duration(shards)*time.Second), // 1 000 per second over all machines
MaxUsers: 1000 / shards,                                        // the cap is per generator
```

Use the same version of jotaro in the program on every machine (the same binary is the simplest way), and the same scenario and step names: the merge matches scenarios and steps by name, so a typo in one build makes a separate row instead of adding to the right one.

## Starting the generators together

The merge overlays the machines by their start times. If they do not run at the same time, the merged series and the merged throughput describe something that never happened. Start them from one place, at one agreed moment, and wait for all of them.

### Clocks

Every result records `started_at`, read from the clock of the machine that ran it (UTC). `jotaro merge` shifts each machine's per-second series by the difference of these start times, in whole seconds, and the run time of the merged result goes from the first start to the last end. So the clocks must agree; keep them in step with NTP (`chrony`, `systemd-timesyncd`, or what your platform provides).

What an offset does depends on how you start:

- **Started by a command at the same real moment** (an `ssh` loop without `-start-at`): a clock that is 3 seconds fast stamps its result 3 seconds late. The merge believes it, shifts that machine's series by 3 seconds, and the run time grows by 3 seconds, although the load really overlapped. Measured here by editing `started_at` of one of three 6-second results by +3 s: the merged run time was 9.0 s instead of 6.0 s, `rps` 3 015 instead of 4 510, and the concurrency went 67, 100, 33 instead of 100 throughout.
- **Started at an agreed wall-clock time** (`-start-at`): a clock that is 3 seconds fast reaches that time 3 seconds early in real time, so that machine starts early. Its `started_at` looks right, so the merge cannot show the offset. The load is misaligned and nothing in the result says so.

Either way the symptom is the same kind of misalignment, and the cure is the same: synchronise the clocks, and check the `started_at` of the results (and the concurrency of the merged series, which should be about the sum of the shares for most of the run) before you trust the merge. Start-up differences below half a second do not shift the series (the shift is rounded to whole seconds), but they still count in the run time.

### With ssh

Build the program once, put the same binary and `testdata` on every machine, and start them from your own machine. `-start-at` gives every machine the same Unix time to begin, far enough ahead for every `ssh` connection to be up. Read the time from one of the generators, so that the lead is measured on a clock the generators agree with.

```sh
hosts="gen1 gen2 gen3"
n=3
start=$(( $(ssh gen1 date +%s) + 20 ))
i=0
pids=""
for h in $hosts; do
  ssh "$h" "cd /opt/loadtest && BASE_URL=https://staging.example.com ./loadtest -shard $i -shards $n -users 300 -start-at $start -o /tmp/result.json" &
  pids="$pids $!"
  i=$((i + 1))
done

fail=0
for p in $pids; do wait "$p" || fail=1; done
mkdir -p results
files=""
for h in $hosts; do
  scp "$h:/tmp/result.json" "results/$h.json" || fail=1
  files="$files results/$h.json"
done
[ "$fail" = 0 ] || { echo "a generator failed: not merging" >&2; exit 1; }
jotaro merge -o results/merged.json $files
```

Merge only when every generator finished and every file arrived. `merge` merges the files it is given: with a glob such as `results/*.json`, a machine whose file did not arrive is simply not in the list, and the result describes a smaller run than you meant. The `wait` per process keeps the exit status of each `ssh`, which a bare `wait` would drop.

### As a Kubernetes Job

An indexed Job starts N pods and gives each its number in `JOB_COMPLETION_INDEX`. The pods are given one start time (`START_AT`), because they are scheduled at different moments (image pulls take time). This manifest is written from the Kubernetes Job documentation and is **not exercised by this repository's tests**; check it on your cluster.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: loadtest
spec:
  completions: 3
  parallelism: 3
  completionMode: Indexed
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: loadtest
          image: registry.example.com/myservice/loadtest:1.0
          env:
            - name: BASE_URL
              value: http://myservice.staging.svc:8080
            - name: START_AT
              value: "1900000000" # Unix time, set when the Job is submitted: now + enough for every pod to start
          command: ["/bin/sh", "-c"]
          args:
            - ./loadtest -shard "$JOB_COMPLETION_INDEX" -shards 3 -users 300 -start-at "$START_AT" -o "/results/gen$JOB_COMPLETION_INDEX.json"
          volumeMounts:
            - name: results
              mountPath: /results
      volumes:
        - name: results
          persistentVolumeClaim:
            claimName: loadtest-results # a volume that all pods can write, or upload from the program instead
```

Set `START_AT` when you submit the Job (`$(( $(date +%s) + 120 ))`, for instance) and leave enough time for the slowest pod to start; a pod that starts after that time begins at once, which shows in the merged result as a machine that is late. Collect `gen0.json` to `gen2.json` from the volume (or the bucket you upload them to) and run `jotaro merge` on them as above. `completions` and `-shards` must be the same number.

## Merging the results

```sh
jotaro merge -o results/merged.json results/gen1.json results/gen2.json results/gen3.json
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'dropped<1' results/merged.json
jotaro report -o results/report.html results/merged.json
```

`merge` writes JSON by default (`-format html` or `text` also work), so the merged result can be merged again, checked, compared and reported like any other. With `-raw` it reads raw data (`Plan.Raw`) instead, every input in the same way. In the library it is `report.MergeReports(reps...)`.

## What the merged numbers mean

| Number | How it is merged |
|---|---|
| Requests, bytes, statuses, errors, `dropped`, `conn_new`, `conn_reused` | Summed |
| Run time and `rps` | The run time goes from the first start to the last end, and `rps` is the total number of requests divided by it. With clocks that agree and a common start it is the run time of one machine; offsets and late starts make it longer and `rps` lower |
| `min`, `max`, `mean` | Exact |
| p50 to p99 | Recomputed by adding the histograms: **within 1% relative error**, even if every input was exact. A distribution merged with an empty one is kept as it is |
| Concurrency of the series | Summed after the shift: an **upper bound**, because the busiest second of one machine need not be the busiest of another |
| Goroutines of the health series | Summed |
| CPU of the health series | The **maximum**: with several generators the one that is closest to saturation decides whether the numbers can be trusted. A single 90% anywhere puts the warning on the merged report |
| Scenarios and steps | Matched by name, in the order they first appear |

Two rules refuse or distort the merge, so know them:

- Results that disagree on the model (`open`) cannot be merged: it is an error, not a guess.
- The merge does not compare the plans. A machine that ran a different rate, a different duration or a different build is added like any other. The merged result does not remember that, because the plan is not recorded in the result ([#92](https://github.com/RIRUa/jotaro/issues/92)), so keep the commands you ran.

Merging is for runs that overlapped. Two runs of the same test one after the other are two runs: merged, they give one run time that spans the gap between them (two 2-second runs, started one after the other, gave a merged run time of 4.6 s), and the series are laid onto a timeline with a hole in it.

## Pitfalls

- **Rounding the split.** Check that the shares add up: `share` does; `1000 / 3` per machine does not. The sum of requests of the merged result is the quickest check of the arrangement (about the single-machine count for the plan, times the machines).
- **Targets and limits are shared.** All generators hit the same target, so the target's limits (and a rate limiter that counts all clients together) see the total. A rate limit per client address sees each generator separately, which is not the same as one client at the total rate.
- **Failures in one machine.** One machine at the limits of its CPU, file descriptors or ports (see [`tuning.md`](tuning.md)) damages the merged percentiles for everyone, and the CPU maximum is how you find it. Look at the individual results, not only the merged one, when the CPU warning is on.
- **Collecting.** The results are small compared to raw data but not tiny (a 6-second run of the example above is about 26 KB per machine, and the JSON grows with the length of the run, [#93](https://github.com/RIRUa/jotaro/issues/93)). Raw data (`Plan.Raw`) is far larger; merge the aggregated results unless you need to re-aggregate.
- **No shared abort.** `AbortOn` is evaluated on each machine against its own numbers. One machine may stop while the others go on, and the merged result then shows a run in which one share ended early.
