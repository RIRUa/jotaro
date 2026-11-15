package report

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// sample builds a one-step report whose requests all take lat.
func sample(lat time.Duration, n int) *Report {
	var reqs []record.Record
	for range n {
		reqs = append(reqs, record.Record{Scenario: "s", Step: "login", Code: 200, Latency: lat})
	}
	return BuildReport(reqs, nil, nil, time.Second)
}

func TestCompareRegression(t *testing.T) {
	c := Compare(sample(100*time.Millisecond, 10), sample(150*time.Millisecond, 10))
	var found bool
	for _, d := range c.Deltas {
		if d.Scope == "overall" && d.Metric == "p95" {
			found = true
			if got := d.Regression(); math.Abs(got-50) > 1e-9 {
				t.Fatalf("regression = %v, want 50", got)
			}
		}
	}
	if !found {
		t.Fatal("overall p95 missing")
	}
	if len(c.Violations(40)) == 0 {
		t.Error("40% gate should fail")
	}
	if len(c.Violations(60)) != 0 {
		t.Error("60% gate should pass")
	}
}

func TestCompareImprovementAndRPS(t *testing.T) {
	c := Compare(sample(100*time.Millisecond, 10), sample(50*time.Millisecond, 20))
	for _, d := range c.Deltas {
		if d.Scope == "overall" && (d.Metric == "p95" || d.Metric == "rps") && d.Regression() >= 0 {
			t.Errorf("%s should be better, regression = %v", d.Metric, d.Regression())
		}
	}
	if len(c.Violations(0)) != 0 {
		t.Error("improvement must not violate")
	}
}

func TestComparePrintZeroBase(t *testing.T) {
	d := Delta{Scope: "overall", Metric: "error_rate", Base: 0, New: 0.1}
	if !math.IsNaN(d.Change()) || !math.IsInf(d.Regression(), 1) {
		t.Fatal("zero base worsening should be NaN change and +Inf regression")
	}
	var buf bytes.Buffer
	Comparison{Deltas: []Delta{d}}.Print(&buf)
	if !strings.Contains(buf.String(), "n/a") || !strings.Contains(buf.String(), "10%") {
		t.Fatalf("output: %s", buf.String())
	}
}
