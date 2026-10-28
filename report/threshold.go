package report

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
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
//   - Ratio: error_rate (network errors and Expect mismatches) / http_4xx_rate / http_5xx_rate
//     (all relative to the number of requests), iteration_fail_rate (relative to the number of iterations)
//   - Count: requests / rps / dropped
//
// {step=...} (optionally together with scenario=...) looks at latency, ratios and requests per step.
// iteration_fail_rate is looked at per scenario with {scenario=...}. rps and dropped are overall only.
type Threshold struct {
	Metric   string
	Scenario string
	Step     string
	Op       string // "<" / "<=" / ">" / ">="
	// Value is held in the unit of Metric: nanoseconds for durations, 0 to 1 for ratios.
	Value float64
}

type metricKind int

const (
	kindDuration metricKind = iota
	kindRatio
	kindNumber
)

// metricScope は指標をどの単位で見られるか。
type metricScope int

const (
	scopeRequests  metricScope = iota // 全体、または {step=...}
	scopeIteration                    // 全体、または {scenario=...}
	scopeGlobal                       // 全体のみ
)

// metric は指標の定義。scope に合った 1 つの関数だけを持つ。
// req と iter の ok が false なら、集計の元になるデータが無い。
type metric struct {
	kind  metricKind
	scope metricScope
	req   func(q RequestStats) (v float64, ok bool)
	iter  func(count, failed int) (v float64, ok bool)
	glob  func(r *Report) float64
}

var (
	latencyParts = map[string]func(q RequestStats) Latency{
		"":           func(q RequestStats) Latency { return q.Latency },
		"corrected_": func(q RequestStats) Latency { return q.Corrected },
		"ttfb_":      func(q RequestStats) Latency { return q.TTFB },
		"dns_":       func(q RequestStats) Latency { return q.DNS },
		"connect_":   func(q RequestStats) Latency { return q.Connect },
		"tls_":       func(q RequestStats) Latency { return q.TLS },
	}
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

// lookupMetric は指標の名前から定義を引く。
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
		return metric{kind: kindRatio, scope: scopeIteration, iter: func(count, failed int) (float64, bool) {
			if count == 0 {
				return 0, false
			}
			return float64(failed) / float64(count), true
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

func requestRatio(n func(q RequestStats) int) metric {
	return metric{kind: kindRatio, scope: scopeRequests, req: func(q RequestStats) (float64, bool) {
		if q.Count == 0 {
			return 0, false
		}
		return float64(n(q)) / float64(q.Count), true
	}}
}

var thresholdOps = []string{"<=", ">=", "<", ">"}

// ParseThreshold turns a string such as "p95<200ms" into a Threshold.
func ParseThreshold(s string) (Threshold, error) {
	fail := func(format string, a ...any) (Threshold, error) {
		return Threshold{}, fmt.Errorf("jotaro: しきい値 %q: %s", s, fmt.Sprintf(format, a...))
	}

	// 演算子は {...} の外にある最初の < か >。
	depth, at := 0, -1
scan:
	for i, c := range s {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
		case '<', '>':
			if depth == 0 {
				at = i
				break scan
			}
		}
	}
	if at < 0 {
		return fail("演算子（<, <=, >, >=）がない")
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
			return fail("} で閉じていない")
		}
		for _, kv := range strings.Split(sel, ",") {
			k, v, ok := strings.Cut(kv, "=")
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if !ok || v == "" {
				return fail("絞り込みは key=value の形で書く: %q", kv)
			}
			switch k {
			case "scenario":
				t.Scenario = v
			case "step":
				t.Step = v
			default:
				return fail("絞り込みのキー %q は使えない（scenario, step）", k)
			}
		}
	}

	m, ok := lookupMetric(t.Metric)
	if !ok {
		return fail("指標 %q を知らない", t.Metric)
	}
	switch m.scope {
	case scopeGlobal:
		if t.Scenario != "" || t.Step != "" {
			return fail("%s は全体でしか見られない", t.Metric)
		}
	case scopeIteration:
		if t.Step != "" {
			return fail("%s は {scenario=...} で絞り込む（step は使えない）", t.Metric)
		}
	case scopeRequests:
		if t.Step == "" && t.Scenario != "" {
			return fail("%s を絞り込むには {step=...} が要る（scenario だけでは絞り込めない）", t.Metric)
		}
	}

	v, err := parseThresholdValue(m.kind, valueText)
	if err != nil {
		return fail("%v", err)
	}
	t.Value = v
	return t, nil
}

func parseThresholdValue(kind metricKind, s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("値がない")
	}
	switch kind {
	case kindDuration:
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("時間として読めない（例: 200ms, 1.5s）")
		}
		return float64(d), nil
	case kindRatio:
		if p, ok := strings.CutSuffix(s, "%"); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return 0, fmt.Errorf("割合として読めない（例: 1%%, 0.01）")
			}
			return v / 100, nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("割合として読めない（例: 1%%, 0.01）")
		}
		return v, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("数として読めない")
	}
	return v, nil
}

// String returns the threshold in a form that ParseThreshold can read.
func (t Threshold) String() string {
	return t.label() + t.Op + t.formatValue(t.Value)
}

// label は指標名と絞り込み（"p95{step=login}"）。
func (t Threshold) label() string {
	var sel []string
	if t.Scenario != "" {
		sel = append(sel, "scenario="+t.Scenario)
	}
	if t.Step != "" {
		sel = append(sel, "step="+t.Step)
	}
	if len(sel) == 0 {
		return t.Metric
	}
	return t.Metric + "{" + strings.Join(sel, ",") + "}"
}

func (t Threshold) formatValue(v float64) string {
	m, ok := lookupMetric(t.Metric)
	if !ok {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	switch m.kind {
	case kindDuration:
		return fmtDur(time.Duration(v))
	case kindRatio:
		return strconv.FormatFloat(v*100, 'g', 4, 64) + "%"
	}
	return strconv.FormatFloat(v, 'g', 4, 64)
}

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

func (r *Report) check(t Threshold) []CheckResult {
	m, ok := lookupMetric(t.Metric)
	if !ok {
		return []CheckResult{{Threshold: t, Note: fmt.Sprintf("指標 %q を知らない", t.Metric)}}
	}
	judge := func(target string, v float64, ok bool) CheckResult {
		if !ok {
			return CheckResult{Threshold: t, Target: target, Note: "データがない"}
		}
		return CheckResult{Threshold: t, Target: target, Actual: v, Pass: t.holds(v)}
	}

	switch m.scope {
	case scopeGlobal:
		return []CheckResult{judge("", m.glob(r), true)}

	case scopeIteration:
		var count, failed int
		matched := false
		for _, sc := range r.Scenarios {
			if t.Scenario != "" && sc.Name != t.Scenario {
				continue
			}
			matched = true
			count += sc.Iterations.Count
			failed += sc.Iterations.Failed
		}
		if !matched {
			return []CheckResult{{Threshold: t, Note: "対象のシナリオがない"}}
		}
		v, ok := m.iter(count, failed)
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
		return []CheckResult{{Threshold: t, Note: "対象のステップがない"}}
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
		fmt.Fprintf(w, "OK: %d 件すべて満たした\n", len(c))
		return
	}
	fmt.Fprintf(w, "NG: %d 件中 %d 件が満たさなかった\n", len(c), failed)
}

func actualText(r CheckResult) string {
	if r.Note != "" {
		return "-"
	}
	return r.Threshold.formatValue(r.Actual)
}
