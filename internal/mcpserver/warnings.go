package mcpserver

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/RIRUa/jotaro/report"
)

// Thresholds of the reviewer-style warnings of summarize_result. Each one is deliberately conservative:
// a warning that fires on a healthy run is worse than none, because the assistant would learn to ignore it.
const (
	// fewSamplesP99 and fewSamplesP95 are the request counts below which the percentile is the maximum
	// (nearest-rank method: rank = ceil(p/100 * n) equals n for n < 100 at p99 and for n < 20 at p95,
	// and the report uses that method). Below them "p99" is one slow request, not a tail.
	fewSamplesP99 = 100
	fewSamplesP95 = 20

	// clientErrorShare is the share of 4xx responses from which a step is reported, and only a step that also has
	// failed runs: a step that expects the rejection (ExpectStatus(404), a wrong password answered with 401) is all 4xx
	// and healthy, while a rate limiter or a missing token fails the checks that expected a success.
	// clientErrorMinCount keeps a handful of responses from deciding it.
	clientErrorShare    = 0.10
	clientErrorMinCount = 10

	// lateArrivalRatio and lateArrivalMinDiff: corrected p95 is at least this many times the latency p95
	// and at least this much longer. The difference floor keeps a 0.2 ms vs 0.5 ms jitter from firing.
	lateArrivalRatio   = 2.0
	lateArrivalMinDiff = 10 * time.Millisecond

	// unstableMinSeconds is the shortest series that is cut in thirds: shorter than that, each third is
	// a few seconds of a handful of requests, which is noise (and the run is short anyway).
	unstableMinSeconds = 30
	// unstableLatencyRatio and unstableMinIncrease: the median of the per-second p95 in the last third is
	// at least this many times the one in the first third and at least this much longer. The median
	// is used so that one slow second (a GC pause, a single stall) does not look like a trend.
	unstableLatencyRatio = 2.0
	unstableMinIncrease  = 10 * time.Millisecond
	// unstableErrorRise is how many percentage points (0.05 = 5) higher the share of failed requests
	// (transport errors and 5xx) in the last third is than in the first.
	unstableErrorRise = 0.05
	// unstableMinFailures is how many failed requests the last third needs for that rise to count: with fewer, one
	// 503 in a 30 s run at 1 request per second is a 10% share and no trend.
	unstableMinFailures = 10
	// steadyLoadTolerance: the trend is only reported when the load itself is steady, that is, the median
	// load (requests started per second in the open model, concurrent iterations in the closed model)
	// of the last third is within this share of the first third's. A deliberate ramp (UserStages, a
	// rising Arrival) makes latency rise by design and is not an unstable system.
	steadyLoadTolerance = 0.25
)

// Codes of the warnings. They are stable so that tests, and later structured output, can refer to them.
const (
	warnFewSamples   = "few_samples"
	warnFailedChecks = "failed_checks"
	warnClientErrors = "client_errors"
	warnLateArrivals = "late_arrivals"
	warnUnstableRun  = "unstable_run"
)

// warning is one reason to distrust a result: a stable code and a message that names the numbers it is based on.
type warning struct {
	Code    string
	Message string
}

// reviewWarnings returns the warnings a reviewer would raise about rep, in a fixed order.
// Each one is computed from the report alone, so it does not depend on whether rep came from JSON or raw data.
func reviewWarnings(rep *report.Report) []warning {
	var ws []warning
	for _, f := range []func(*report.Report) *warning{
		fewSamplesWarning,
		failedChecksWarning,
		clientErrorsWarning,
		lateArrivalsWarning,
		unstableRunWarning,
	} {
		if w := f(rep); w != nil {
			ws = append(ws, *w)
		}
	}
	return ws
}

// stepTarget is the name used for a step in messages, the same as the target of check_result.
func stepTarget(sc report.ScenarioReport, st report.StepStats) string {
	return sc.Name + "/" + st.Name
}

// requestErrors is the number of requests that ended in a transport-level error.
func requestErrors(q report.RequestStats) int {
	n := 0
	for _, c := range q.Errors {
		n += c
	}
	return n
}

// fewSamplesWarning reports a run, or the steps of a run, with too few requests for p95 or p99.
// When the whole run is too small every step is, so only the run is named.
func fewSamplesWarning(rep *report.Report) *warning {
	// Nothing was recorded: the "no requests" warning already says so.
	if n := rep.Requests.Count; n > 0 && n < fewSamplesP99 {
		return &warning{warnFewSamples, fmt.Sprintf(
			"only %d requests were recorded: %s; read it as a smoke test, not as a percentile, and re-run longer before comparing it with another result",
			n, percentileCaveat(n))}
	}
	var parts []string
	for _, sc := range rep.Scenarios {
		for _, st := range sc.Steps {
			if n := st.Requests.Count; n > 0 && n < fewSamplesP99 {
				parts = append(parts, fmt.Sprintf("%s (%d requests, %s)", stepTarget(sc, st), n, percentileCaveat(n)))
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &warning{warnFewSamples, "too few requests in some steps for their percentiles: " + strings.Join(parts, "; ") +
		"; a step percentile from so few requests is one slow request, so do not compare it with a threshold or another run"}
}

// percentileCaveat says which percentiles of n requests are no more than the slowest request.
func percentileCaveat(n int) string {
	if n < fewSamplesP95 {
		return "p95 and p99 are the slowest request"
	}
	return "p99 is the slowest request"
}

// failedChecksWarning reports failures that the error rate does not show. error_rate counts transport-level errors
// only, so a step whose function returned an error (a failed Expect* check, a missing JSON field) while no request
// failed at the transport level looks like a run with 0% errors.
func failedChecksWarning(rep *report.Report) *warning {
	var parts []string
	for _, sc := range rep.Scenarios {
		flagged := false
		for _, st := range sc.Steps {
			// Step failures that a transport error accounts for are in the error rate already.
			if hidden := st.Failed - requestErrors(st.Requests); hidden > 0 {
				flagged = true
				parts = append(parts, fmt.Sprintf("step %s failed %d of %d runs, at least %d of them with no request error", stepTarget(sc, st), st.Failed, st.Count, hidden))
			}
		}
		// A scenario can return its own error without any failed step.
		if !flagged && sc.Iterations.Failed > 0 && rep.Requests.Count > 0 && requestErrors(rep.Requests) == 0 {
			parts = append(parts, fmt.Sprintf("scenario %s failed %d of %d iterations", sc.Name, sc.Iterations.Failed, sc.Iterations.Count))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &warning{warnFailedChecks, "failures that error_rate does not show (it counts request errors such as timeouts, not failed Expect* checks): " +
		strings.Join(parts, "; ") + "; judge iteration_fail_rate, and do not read \"no errors\" as \"all answers were right\""}
}

// clientErrorShareOf is the share of q's requests that got a 4xx response.
// A request that failed with a transport error has no status class, so it is not in the numerator.
func clientErrorShareOf(q report.RequestStats) (n int, share float64) {
	n = q.Status["4xx"]
	if q.Count == 0 {
		return n, 0
	}
	return n, float64(n) / float64(q.Count)
}

// clientErrorsWarning reports steps where many responses are 4xx and some runs of the step failed. A step without
// failed runs is left alone even when all of its responses are 4xx: it checks for that status on purpose, or has no
// Expect* check to say otherwise. The status classes are all that a result keeps, so 429 (a rate limiter) cannot be
// told from 401, 403 or 404 here; the message names the cases.
func clientErrorsWarning(rep *report.Report) *warning {
	var parts []string
	for _, sc := range rep.Scenarios {
		for _, st := range sc.Steps {
			if st.Failed == 0 {
				continue
			}
			if n, share := clientErrorShareOf(st.Requests); n >= clientErrorMinCount && share >= clientErrorShare {
				parts = append(parts, fmt.Sprintf("%s %.0f%% (%d of %d)", stepTarget(sc, st), share*100, n, st.Requests.Count))
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &warning{warnClientErrors, "4xx responses in steps that failed (share of the step's requests): " + strings.Join(parts, "; ") + ": the latency of a rejected request is not the latency of the feature. " +
		"Either the scenario sends something the target rejects (a missing or expired token, a wrong path), or the target throttled the load " +
		"(429 from a rate limiter), in which case the test measured the limiter; check the scenario and the target's rate limit before trusting the throughput"}
}

// lateArrivalsWarning reports an open-model run in which the corrected latency is far above the latency: the iterations
// started later than scheduled, so the latency alone understates what users would have seen. Scheduled belongs to the
// first request of an iteration, so time spent before it (Think, other work in the scenario) counts as lateness too.
func lateArrivalsWarning(rep *report.Report) *warning {
	if !rep.Open || rep.Requests.Count == 0 {
		return nil
	}
	lat, cor := rep.Requests.Latency.P95, rep.Requests.Corrected.P95
	if float64(cor) < lateArrivalRatio*float64(lat) || cor-lat < lateArrivalMinDiff {
		return nil
	}
	return &warning{warnLateArrivals, fmt.Sprintf(
		"arrivals started late: corrected p95 is %s against a latency p95 of %s: the target or the load generator could not keep the arrival rate, so the latency alone understates what users would have waited. Judge it by corrected_p95, and check dropped; time the scenario spends before its first request (Think, other work) is counted as lateness too",
		fmtMs(cor), fmtMs(lat))}
}

// unstableRunWarning reports a run that got worse while it ran: a single whole-run percentile then describes no steady
// state, and a longer run would look worse. The latency of the last third of the per-second series is compared with the
// first third's, and so is the share of failed requests. It is skipped when the load itself changed (a ramp).
func unstableRunWarning(rep *report.Report) *warning {
	n := len(rep.Series)
	if n < unstableMinSeconds {
		return nil
	}
	first, last := rep.Series[:n/3], rep.Series[n-n/3:]
	if !steadyLoad(first, last, rep.Open) {
		return nil
	}
	var reasons []string
	p95First, p95Last := medianP95(first), medianP95(last)
	if float64(p95Last) >= unstableLatencyRatio*float64(p95First) && p95Last-p95First >= unstableMinIncrease {
		reasons = append(reasons, fmt.Sprintf("the median per-second p95 rose from %s in the first third of the run to %s in the last third", fmtMs(p95First), fmtMs(p95Last)))
	}
	errFirst, errLast := failureShare(first), failureShare(last)
	if failures(last) >= unstableMinFailures && errLast-errFirst >= unstableErrorRise {
		reasons = append(reasons, fmt.Sprintf("the share of failed requests (errors and 5xx) rose from %.1f%% to %.1f%%", errFirst*100, errLast*100))
	}
	if len(reasons) == 0 {
		return nil
	}
	return &warning{warnUnstableRun, "the run degraded while it ran: " + strings.Join(reasons, "; ") +
		" under a steady load. One whole-run percentile describes no steady state here, and a longer run would look worse; read the series or the worst second"}
}

// steadyLoad reports whether the load of the last third is within steadyLoadTolerance of the first third's.
func steadyLoad(first, last []report.SeriesPoint, open bool) bool {
	load := func(p report.SeriesPoint) int {
		if open {
			return p.Requests // arrivals are scheduled: requests started per second follows the rate, not the latency
		}
		return p.Concurrency // a closed model runs a fixed number of users: requests per second follows the latency
	}
	a, b := medianInt(first, load), medianInt(last, load)
	if a < 1 { // no load in the first third: there is nothing to compare with
		return false
	}
	return float64(b) >= (1-steadyLoadTolerance)*float64(a) && float64(b) <= (1+steadyLoadTolerance)*float64(a)
}

// medianP95 is the median of the per-second p95 over the seconds that have requests.
func medianP95(pts []report.SeriesPoint) time.Duration {
	var v []time.Duration
	for _, p := range pts {
		if p.Latency.Count > 0 {
			v = append(v, p.Latency.P95)
		}
	}
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// medianInt is the median of f over pts.
func medianInt(pts []report.SeriesPoint, f func(report.SeriesPoint) int) int {
	if len(pts) == 0 {
		return 0
	}
	v := make([]int, len(pts))
	for i, p := range pts {
		v[i] = f(p)
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// failures is the number of requests started in pts that ended in a transport error or a 5xx.
func failures(pts []report.SeriesPoint) int {
	n := 0
	for _, p := range pts {
		n += p.Errors + p.HTTP5xx
	}
	return n
}

// failureShare is the share of requests started in pts that ended in a transport error or a 5xx.
func failureShare(pts []report.SeriesPoint) float64 {
	var req, failed int
	for _, p := range pts {
		req += p.Requests
		failed += p.Errors + p.HTTP5xx
	}
	if req == 0 {
		return 0
	}
	return float64(failed) / float64(req)
}

// fmtMs writes d as milliseconds for a message.
func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%gms", ms(d))
}
