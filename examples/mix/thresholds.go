package main

// thresholds are the pass/fail goals of the run. The run fails (exit status 1) when one is not met.
//
// Mind what each metric counts:
//   - error_rate is the share of requests that ended in a transport-level error (timeout, connection refused,
//     DNS failure and so on). A failed Expect* check is not counted as a request error: it fails the step with
//     expect_mismatch, which is listed in the step's errors, and it reaches iteration_fail_rate when the scenario
//     returns that error, as the scenarios here do.
//   - http_5xx_rate looks at the status codes, so it sees a broken server even without an Expect* check.
//   - {scenario=...} narrows a metric to one scenario of the mix, and {step=...} to one step.
var thresholds = []string{
	// The steps users wait for, per scenario.
	"p95{scenario=browse,step=list}<150ms",
	"p95{scenario=buy,step=order}<250ms",
	// A whole iteration, Think time included, so slow steps and slow users show up here.
	"iteration_p95{scenario=browse}<1s",
	"iteration_p95{scenario=buy}<2s",
	// Network errors and 5xx responses.
	"error_rate<1%",
	"http_5xx_rate<1%",
	// Expect mismatches and anything else that made a scenario give up.
	"iteration_fail_rate{scenario=browse}<1%",
	"iteration_fail_rate{scenario=buy}<1%",
}
