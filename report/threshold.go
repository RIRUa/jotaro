package report

import (
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Threshold is a condition that the result must meet. It is used in CI to fail the build when things get slower.
//
// In a string it is written in the following form (ParseThreshold).
//
//	p95<200ms
//	error_rate<1%
//	p99{step=login}<100ms
//	iteration_fail_rate{scenario=checkout}<=5%
//
// The available metrics are:
//
//   - Latency: [phase_]statistic. The statistic is one of min / mean / p50 / p90 / p95 / p99 / max.
//     The phase is none (the whole request) / corrected_ (after Coordinated Omission correction) /
//     ttfb_ / dns_ / connect_ / tls_. Examples: p95, corrected_p99, ttfb_p50
//   - Ratio: error_rate / http_4xx_rate / http_5xx_rate (all relative to the number of requests),
//     iteration_fail_rate (relative to the number of iterations)
//   - Scenario latency: iteration_[active_]statistic, the time of a whole scenario iteration.
//     iteration_ includes Think time and iteration_active_ leaves it out. Examples: iteration_p95, iteration_active_p99
//   - Count: requests / rps / dropped
//
// error_rate is the share of requests that ended in a transport-level error (timeout, connection refused,
// DNS failure and so on). A failed Expect* check is not counted as a request error: it fails the step with
// expect_mismatch, which is listed in the step's errors, and it reaches iteration_fail_rate when the scenario
// returns that error. An HTTP error status is counted by http_4xx_rate / http_5xx_rate.
//
// A step or scenario name that contains a comma, a quote, an unbalanced brace, < or > (or spaces at its ends) is written
// as a Go double-quoted string: p95{step="list, then order"}<1s. Inside the quotes everything is literal, and \" and \\ are
// escapes. An unquoted name is read as before: up to the next comma, with the spaces around it trimmed. A name written
// as a complete quoted string is always the unquoted text, so to select a step whose own name includes quote characters,
// quote it: p95{step="\"quoted\""}<1s. Threshold.String quotes a name whenever it needs to.
//
// {step=...} (optionally together with scenario=...) looks at latency, ratios and requests per step.
// iteration_fail_rate and the scenario latencies are looked at per scenario with {scenario=...}. rps and dropped are overall only.
type Threshold struct {
	Metric   string
	Scenario string
	Step     string
	Op       string // "<" / "<=" / ">" / ">="
	// Value is held in the unit of Metric: nanoseconds for durations, 0 to 1 for ratios.
	Value float64
}

// metricKind is the unit of a metric's value. It decides how the right-hand side of a threshold
// is parsed (parseThresholdValue) and how values are shown (formatValue).
type metricKind int

const (
	kindDuration metricKind = iota // "200ms", held in nanoseconds
	kindRatio                      // "1%" or "0.01", held as 0 to 1
	kindNumber                     // a plain number, such as requests per second
)

// metricScope is the unit a metric can be evaluated at.
type metricScope int

const (
	scopeRequests  metricScope = iota // whole run, or {step=...}
	scopeIteration                    // whole run, or {scenario=...}
	scopeGlobal                       // whole run only
)

// metric is the definition of a metric. It holds only the one function that matches its scope.
// If ok from req or iter is false, there is no data to aggregate.
type metric struct {
	kind  metricKind
	scope metricScope
	req   func(q RequestStats) (v float64, ok bool)
	iter  func(it IterationStats) (v float64, ok bool)
	glob  func(r *Report) float64
}

// The two tables below build metric names such as "corrected_p95" from a prefix (which distribution)
// and a statistic (which number of it).
var (
	// latencyParts maps a metric prefix to the distribution it reads.
	latencyParts = map[string]func(q RequestStats) Latency{
		"":           func(q RequestStats) Latency { return q.Latency },
		"corrected_": func(q RequestStats) Latency { return q.Corrected },
		"ttfb_":      func(q RequestStats) Latency { return q.TTFB },
		"dns_":       func(q RequestStats) Latency { return q.DNS },
		"connect_":   func(q RequestStats) Latency { return q.Connect },
		"tls_":       func(q RequestStats) Latency { return q.TLS },
	}
	// latencyStats maps a metric suffix to the statistic it reads.
	latencyStats = map[string]func(l Latency) time.Duration{
		"min":  func(l Latency) time.Duration { return l.Min },
		"mean": func(l Latency) time.Duration { return l.Mean },
		"p50":  func(l Latency) time.Duration { return l.P50 },
		"p90":  func(l Latency) time.Duration { return l.P90 },
		"p95":  func(l Latency) time.Duration { return l.P95 },
		"p99":  func(l Latency) time.Duration { return l.P99 },
		"max":  func(l Latency) time.Duration { return l.Max },
	}
)

// lookupMetric finds the definition from a metric name.
func lookupMetric(name string) (metric, bool) {
	switch name {
	case "error_rate":
		return requestRatio(func(q RequestStats) int {
			n := 0
			for _, c := range q.Errors {
				n += c
			}
			return n
		}), true
	case "http_4xx_rate":
		return requestRatio(func(q RequestStats) int { return q.Status["4xx"] }), true
	case "http_5xx_rate":
		return requestRatio(func(q RequestStats) int { return q.Status["5xx"] }), true
	case "iteration_fail_rate":
		return metric{kind: kindRatio, scope: scopeIteration, iter: func(it IterationStats) (float64, bool) {
			if it.Count == 0 {
				return 0, false
			}
			return float64(it.Failed) / float64(it.Count), true
		}}, true
	case "requests":
		return metric{kind: kindNumber, scope: scopeRequests, req: func(q RequestStats) (float64, bool) {
			return float64(q.Count), true
		}}, true
	case "rps":
		return metric{kind: kindNumber, scope: scopeGlobal, glob: (*Report).RPS}, true
	case "dropped":
		return metric{kind: kindNumber, scope: scopeGlobal, glob: func(r *Report) float64 { return float64(r.Dropped) }}, true
	}
	if m, ok := iterationLatencyMetric(name); ok {
		return m, true
	}
	for prefix, part := range latencyParts {
		stat, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		if pick, ok := latencyStats[stat]; ok {
			return metric{kind: kindDuration, scope: scopeRequests, req: func(q RequestStats) (float64, bool) {
				l := part(q)
				if l.Count == 0 {
					return 0, false
				}
				return float64(pick(l)), true
			}}, true
		}
	}
	return metric{}, false
}

// iterationLatencyMetric resolves "iteration_<stat>" (Total, with Think) and "iteration_active_<stat>" (Active, without Think).
func iterationLatencyMetric(name string) (metric, bool) {
	rest, ok := strings.CutPrefix(name, "iteration_")
	if !ok {
		return metric{}, false
	}
	part := func(it IterationStats) Latency { return it.Total }
	if r, ok := strings.CutPrefix(rest, "active_"); ok {
		rest, part = r, func(it IterationStats) Latency { return it.Active }
	}
	pick, ok := latencyStats[rest]
	if !ok {
		return metric{}, false
	}
	return metric{kind: kindDuration, scope: scopeIteration, iter: func(it IterationStats) (float64, bool) {
		l := part(it)
		if l.Count == 0 {
			return 0, false
		}
		return float64(pick(l)), true
	}}, true
}

// requestRatio is a ratio metric: n(q) of the requests, as a share of all requests.
// With no requests there is nothing to divide, so it reports no data.
func requestRatio(n func(q RequestStats) int) metric {
	return metric{kind: kindRatio, scope: scopeRequests, req: func(q RequestStats) (float64, bool) {
		if q.Count == 0 {
			return 0, false
		}
		return float64(n(q)) / float64(q.Count), true
	}}
}

// thresholdOps lists the operators. Two-character ones come first so that "<=" is not read as "<".
var thresholdOps = []string{"<=", ">=", "<", ">"}

// ParseThreshold turns a string such as "p95<200ms" into a Threshold.
//
// The value must be one that a measurement can take, so that a typo cannot make a threshold that is never met
// or always met. ParseThreshold returns an error that names the expression and the reason for NaN and
// infinities (including a number too large for float64, such as 1e400), for a negative value of any kind
// ("p95<-1s", "rps>-1", "error_rate<-5%"; -0 is zero and accepted) and for a ratio above 100%
// ("error_rate<500%", or above 1 in the plain form "error_rate<1.5"). 0, 100% and 1 are accepted.
// The same parser reads Plan.AbortOn, so "error_rate>500%" (a rule that could never fire) is an error there too.
func ParseThreshold(s string) (Threshold, error) {
	fail := func(format string, a ...any) (Threshold, error) {
		return Threshold{}, fmt.Errorf("jotaro: threshold %q: %s", s, fmt.Sprintf(format, a...))
	}

	// The operator is the first < or > outside {...} and outside quoted selector values.
	at := findOperator(s)
	if at < 0 {
		if strings.ContainsAny(s, "<>") {
			return fail("no operator (<, <=, >, >=) outside {...}; quote a step or scenario name that contains an unbalanced brace, <, > or a comma: step=\"...\"")
		}
		return fail("no operator (<, <=, >, >=)")
	}
	left := strings.TrimSpace(s[:at])
	right := s[at:]
	var op string
	for _, o := range thresholdOps {
		if strings.HasPrefix(right, o) {
			op = o
			break
		}
	}
	valueText := strings.TrimSpace(strings.TrimPrefix(right, op))

	t := Threshold{Op: op}
	name, sel, hasSel := strings.Cut(left, "{")
	t.Metric = strings.TrimSpace(name)
	if hasSel {
		sel, ok := strings.CutSuffix(sel, "}")
		if !ok {
			return fail("not closed with }")
		}
		filters, err := parseSelector(sel)
		if err != nil {
			return fail("%v", err)
		}
		for _, f := range filters {
			switch f.key {
			case "scenario":
				t.Scenario = f.value
			case "step":
				t.Step = f.value
			default:
				return fail("filter key %q is not allowed (scenario, step)", f.key)
			}
		}
	}

	m, ok := lookupMetric(t.Metric)
	if !ok {
		return fail("unknown metric %q", t.Metric)
	}
	switch m.scope {
	case scopeGlobal:
		if t.Scenario != "" || t.Step != "" {
			return fail("%s can only be looked at for the whole run", t.Metric)
		}
	case scopeIteration:
		if t.Step != "" {
			return fail("%s is filtered with {scenario=...} (step cannot be used)", t.Metric)
		}
	case scopeRequests:
		if t.Step == "" && t.Scenario != "" {
			return fail("filtering %s needs {step=...} (scenario alone is not enough)", t.Metric)
		}
	}

	v, err := parseThresholdValue(m.kind, valueText)
	if err != nil {
		return fail("%v", err)
	}
	t.Value = v
	return t, nil
}

// selectorFilter is one key=value of a {...} selector.
type selectorFilter struct{ key, value string }

// readQuoted reads a quoted selector value at the start of s, which must begin with `"`. A quoted value is a
// Go double-quoted string literal (`"list, then order"`, `"say \"hi\""`) that ends the value: after optional spaces
// comes `,` (another filter), the `}` that closes the selector (followed by the operator), or the end of s.
// It returns the unquoted text and the length of the literal in s.
// Anything else (an unterminated quote, a bad escape, text after the closing quote) is not a quoted value,
// and the caller reads the text as an unquoted name, as before quoting existed.
func readQuoted(s string) (val string, n int, ok bool) {
	lit, err := strconv.QuotedPrefix(s)
	if err != nil || lit[0] != '"' {
		return "", 0, false
	}
	rest := strings.TrimLeftFunc(s[len(lit):], unicode.IsSpace)
	if after, closed := strings.CutPrefix(rest, "}"); closed {
		rest = strings.TrimLeftFunc(after, unicode.IsSpace)
		if rest != "" && rest[0] != '<' && rest[0] != '>' {
			return "", 0, false
		}
	} else if rest != "" && rest[0] != ',' {
		return "", 0, false
	}
	val, err = strconv.Unquote(lit)
	if err != nil {
		return "", 0, false
	}
	return val, len(lit), true
}

// findOperator returns the index of the first < or > in s that is outside {...} and outside a quoted value
// of the selector, or -1. Braces are counted by depth, so a nested {id} in a name is fine, and the text of
// a quoted value is skipped as a whole.
func findOperator(s string) int {
	depth := 0
	keySeen := false    // the = of the current filter has been seen (only filters of the first {...} are read)
	valueStart := false // only spaces since that =, so a " here opens a quoted value
	skipTo := 0
	for i, c := range s {
		if i < skipTo {
			continue
		}
		if valueStart && c == '"' {
			if _, n, ok := readQuoted(s[i:]); ok {
				skipTo = i + n
				valueStart = false
				continue
			}
		}
		switch {
		case unicode.IsSpace(c):
			// Spaces between = and the value keep valueStart.
		case c == '{':
			depth++
			valueStart = false
		case c == '}':
			depth--
			valueStart = false
		case c == ',':
			if depth >= 1 {
				keySeen = false
			}
			valueStart = false
		case c == '=':
			valueStart = depth >= 1 && !keySeen
			if depth >= 1 {
				keySeen = true
			}
		case c == '<' || c == '>':
			if depth == 0 {
				return i
			}
			valueStart = false
		default:
			valueStart = false
		}
	}
	return -1
}

// parseSelector splits the inside of {...} into key=value filters, separated by commas.
// A value is either quoted (see readQuoted), in which case it is taken literally, spaces and all,
// or the text up to the next comma with the spaces around it trimmed.
func parseSelector(sel string) ([]selectorFilter, error) {
	var out []selectorFilter
	rest := sel
	for {
		eq := strings.IndexAny(rest, "=,")
		if eq < 0 || rest[eq] != '=' {
			frag, _, _ := strings.Cut(rest, ",")
			return nil, fmt.Errorf("a filter must be written as key=value: %q", frag)
		}
		key := strings.TrimSpace(rest[:eq])
		rest = strings.TrimLeftFunc(rest[eq+1:], unicode.IsSpace)

		var value string
		var more, quoted bool
		if strings.HasPrefix(rest, `"`) {
			if v, n, ok := readQuoted(rest); ok {
				after := strings.TrimLeftFunc(rest[n:], unicode.IsSpace)
				if after == "" || after[0] == ',' {
					if v == "" {
						return nil, fmt.Errorf("the name of %s is empty (\"\")", key)
					}
					value, quoted = v, true
					rest, more = strings.CutPrefix(after, ",")
				}
			}
		}
		if !quoted {
			var raw string
			raw, rest, more = strings.Cut(rest, ",")
			value = strings.TrimSpace(raw)
			if value == "" {
				return nil, fmt.Errorf("a filter must be written as key=value: %q", key+"="+raw)
			}
		}
		out = append(out, selectorFilter{key, value})
		if !more {
			return out, nil
		}
	}
}

// parseThresholdValue parses the right-hand side of a threshold in the unit of kind
// (for example "200ms" for a duration, "1%" for a ratio).
//
// The result is always finite and not negative, because no metric can be NaN, infinite or negative:
// such a threshold could never be met (or, for a ratio above 100%, would always be met), which is a
// configuration mistake. A ratio is also at most 1 (100%).
func parseThresholdValue(kind metricKind, s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("no value")
	}
	switch kind {
	case kindDuration:
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("cannot be read as a duration (e.g. 200ms, 1.5s)")
		}
		return checkThresholdValue(float64(d), s)
	case kindRatio:
		p, isPercent := strings.CutSuffix(s, "%")
		text := s
		if isPercent {
			text = strings.TrimSpace(p)
		}
		v, err := strconv.ParseFloat(text, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, fmt.Errorf("cannot be read as a ratio (e.g. 1%%, 0.01)")
		}
		if isPercent {
			v /= 100
		}
		v, err = checkThresholdValue(v, s)
		if err != nil {
			return 0, err
		}
		if v > 1 {
			return 0, fmt.Errorf("ratio %q is above 100%% (above 1 in the plain form)", s)
		}
		return v, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("cannot be read as a number")
	}
	return checkThresholdValue(v, s)
}

// checkThresholdValue rejects a value that no measurement can ever take: NaN, an infinity (this includes a
// number too large for float64, which strconv reports as a range error with an infinite result) and a negative
// number. A negative zero is normalized to zero so that equal thresholds compare equal. text is the value as written.
func checkThresholdValue(v float64, text string) (float64, error) {
	switch {
	case math.IsNaN(v):
		return 0, fmt.Errorf("value %q is NaN; use a finite number", text)
	case math.IsInf(v, 0):
		return 0, fmt.Errorf("value %q is infinite or out of range; use a finite number", text)
	case v < 0:
		return 0, fmt.Errorf("value %q is negative; no metric can be negative", text)
	case v == 0:
		return 0, nil
	}
	return v, nil
}

// String returns the threshold in a form that ParseThreshold reads back as the same threshold.
// Unlike the values shown in reports, the value is exact: it is never rounded and a zero duration
// is written as "0s".
func (t Threshold) String() string {
	return t.label() + t.Op + t.exactValue(t.Value)
}

// label is the metric name with its selector ("p95{step=login}"). A name that the parser would not read
// back as it is (a comma, a quote, an unbalanced brace, spaces at the ends, an unprintable character)
// is written as a quoted Go string.
func (t Threshold) label() string {
	var sel []string
	if t.Scenario != "" {
		sel = append(sel, "scenario="+selectorName(t.Scenario))
	}
	if t.Step != "" {
		sel = append(sel, "step="+selectorName(t.Step))
	}
	if len(sel) == 0 {
		return t.Metric
	}
	return t.Metric + "{" + strings.Join(sel, ",") + "}"
}

// selectorName writes a scenario or step name as a selector value: as is when ParseThreshold reads it back
// unchanged, otherwise quoted.
func selectorName(name string) string {
	if needsQuote(name) {
		return strconv.Quote(name)
	}
	return name
}

// needsQuote reports whether name cannot be written unquoted in a selector: it has a comma or a quote,
// leading or trailing spaces, a character that is not printable (so it would be invisible or break the line),
// or a brace, < or > that is not inside a balanced {...} pair.
func needsQuote(name string) bool {
	if name != strings.TrimSpace(name) || !utf8.ValidString(name) {
		return true
	}
	depth := 0
	for _, c := range name {
		switch {
		case c == ',' || c == '"':
			return true
		case c != ' ' && !strconv.IsPrint(c):
			return true
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth < 0 {
				return true
			}
		case (c == '<' || c == '>') && depth == 0:
			return true
		}
	}
	return depth != 0
}

// exactValue writes v in the unit of the threshold's metric so that parseThresholdValue reads it back
// as the same value, for example "200ms", "1.5%" or "1234567". It is used by String; values for
// people to read go through formatValue.
func (t Threshold) exactValue(v float64) string {
	m, ok := lookupMetric(t.Metric)
	if !ok {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	switch m.kind {
	case kindDuration:
		return durationFromFloat(v).String()
	case kindRatio:
		// A percentage is easier to read ("1%"), but v*100 can pick up a rounding error (0.07*100 is
		// 7.000000000000001). Try the percentage cleaned to 12 significant digits first, then the exact
		// one, and keep the first that reads back as exactly v. Otherwise write a plain ratio.
		for _, digits := range []int{12, 17} {
			clean, _ := strconv.ParseFloat(strconv.FormatFloat(v*100, 'g', digits, 64), 64)
			if clean/100 == v {
				return strconv.FormatFloat(clean, 'f', -1, 64) + "%"
			}
		}
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// durationFromFloat converts v nanoseconds to a Duration. Converting a float that does not fit in int64 is
// implementation-defined (float64(math.MaxInt64) is 2^63, and it wraps to math.MinInt64 on amd64), so a value
// out of range, such as the mean of two huge latencies or a value from a damaged result file, is clamped to the
// largest or smallest Duration, and NaN becomes 0.
func durationFromFloat(v float64) time.Duration {
	switch {
	case math.IsNaN(v):
		return 0
	case v >= math.MaxInt64:
		return time.Duration(math.MaxInt64)
	case v <= math.MinInt64:
		return time.Duration(math.MinInt64)
	}
	return time.Duration(v)
}

// formatValue shows v in the unit of the threshold's metric for people to read, for example "200ms" or "1.5%".
// It rounds, and it is not meant to be parsed again (see exactValue).
func (t Threshold) formatValue(v float64) string {
	m, ok := lookupMetric(t.Metric)
	if !ok {
		return fmtNumber(v)
	}
	switch m.kind {
	case kindDuration:
		return fmtDur(durationFromFloat(v))
	case kindRatio:
		return fmtNumber(v*100) + "%"
	}
	return fmtNumber(v)
}

// fmtNumber shows a count or a rate with at most two decimals and never in exponent notation
// ("1234567", "12345.6", "0.5"). A value too small for two decimals keeps three significant digits instead of becoming "0".
func fmtNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if v != 0 && (s == "0" || s == "-0") {
		small, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 3, 64), 64)
		return strconv.FormatFloat(small, 'f', -1, 64)
	}
	return s
}

// holds reports whether actual satisfies the threshold (actual Op Value).
func (t Threshold) holds(actual float64) bool {
	switch t.Op {
	case "<":
		return actual < t.Value
	case "<=":
		return actual <= t.Value
	case ">":
		return actual > t.Value
	case ">=":
		return actual >= t.Value
	}
	return false
}

// CheckResult is the result of checking one threshold against one target.
type CheckResult struct {
	Threshold Threshold
	// Target is what was actually looked at: the scenario and step that matched the filter, such as "checkout/login". Empty for the whole run.
	Target string
	Actual float64
	Pass   bool
	// Note explains why Pass is false when the reason is not a comparison of values (no data, no target).
	Note string
}

// Checks is the result of Check.
type Checks []CheckResult

// Passed reports whether every threshold was met.
func (c Checks) Passed() bool {
	return !slices.ContainsFunc(c, func(r CheckResult) bool { return !r.Pass })
}

// Check checks whether r meets all of ts.
//
// A threshold built by ParseThreshold always has a finite, non-negative value (and a ratio of at most 1),
// so a mistake such as "error_rate<500%" is rejected there instead of passing here. A Threshold assembled by hand
// with a NaN, infinite, negative or above-100% value is not checked again: such a value is the caller's mistake.
//
// When {step=...} matches steps of the same name in several scenarios, each is checked separately.
// When nothing matches the filter, or there is no data to aggregate (such as zero requests),
// the threshold counts as not met, so that a configuration mistake or a load test that never ran cannot pass.
func (r *Report) Check(ts ...Threshold) Checks {
	var out Checks
	for _, t := range ts {
		out = append(out, r.check(t)...)
	}
	return out
}

// check evaluates one threshold. The metric's scope decides how many results come back:
// global and iteration metrics give one, and a request metric gives one for the whole run,
// or one per matching step when the threshold selects a step.
// Anything that cannot be judged (unknown metric, no matching scenario or step, no data)
// comes back as a failing result with a Note, so a typo in CI cannot pass silently.
func (r *Report) check(t Threshold) []CheckResult {
	m, ok := lookupMetric(t.Metric)
	if !ok {
		return []CheckResult{{Threshold: t, Note: fmt.Sprintf("unknown metric %q", t.Metric)}}
	}
	judge := func(target string, v float64, ok bool) CheckResult {
		if !ok {
			return CheckResult{Threshold: t, Target: target, Note: "no data"}
		}
		return CheckResult{Threshold: t, Target: target, Actual: v, Pass: t.holds(v)}
	}

	switch m.scope {
	case scopeGlobal:
		return []CheckResult{judge("", m.glob(r), true)}

	case scopeIteration:
		var agg IterationStats
		matched := false
		for _, sc := range r.Scenarios {
			if t.Scenario != "" && sc.Name != t.Scenario {
				continue
			}
			matched = true
			// Merging keeps the histograms, so percentiles over several scenarios are computed from the combined distribution.
			agg = mergeIterations(agg, sc.Iterations)
		}
		if !matched {
			return []CheckResult{{Threshold: t, Note: "no matching scenario"}}
		}
		v, ok := m.iter(agg)
		return []CheckResult{judge(t.Scenario, v, ok)}
	}

	if t.Step == "" {
		v, ok := m.req(r.Requests)
		return []CheckResult{judge("", v, ok)}
	}
	var out []CheckResult
	for _, sc := range r.Scenarios {
		if t.Scenario != "" && sc.Name != t.Scenario {
			continue
		}
		for _, st := range sc.Steps {
			if st.Name != t.Step {
				continue
			}
			v, ok := m.req(st.Requests)
			out = append(out, judge(sc.Name+"/"+st.Name, v, ok))
		}
	}
	if len(out) == 0 {
		return []CheckResult{{Threshold: t, Note: "no matching step"}}
	}
	return out
}

// Print writes the results one per line, and the overall outcome at the end.
func (c Checks) Print(w io.Writer) {
	for _, r := range c {
		mark := "PASS"
		if !r.Pass {
			mark = "FAIL"
		}
		detail := ""
		switch {
		case r.Note != "":
			detail = "  (" + r.Note + ")"
		case r.Target != "":
			detail = "  [" + r.Target + "]"
		}
		fmt.Fprintf(w, "%s  %-40s actual=%s%s\n", mark, r.Threshold.String(), actualText(r), detail)
	}
	failed := 0
	for _, r := range c {
		if !r.Pass {
			failed++
		}
	}
	if failed == 0 {
		fmt.Fprintf(w, "OK: all %d met\n", len(c))
		return
	}
	fmt.Fprintf(w, "NG: %d of %d not met\n", failed, len(c))
}

// actualText is the measured value for display, or "-" when the result could not be judged.
func actualText(r CheckResult) string {
	if r.Note != "" {
		return "-"
	}
	return r.Threshold.formatValue(r.Actual)
}
