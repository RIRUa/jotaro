package jotaro

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func sampleReport(open bool) *Report {
	reqs := []Record{
		{Scenario: "s<b>", Step: "a", Code: 200, Latency: 10 * time.Millisecond, TTFB: 5 * time.Millisecond},
		{Scenario: "s<b>", Step: "a", Code: 500, Latency: 40 * time.Millisecond, TTFB: 20 * time.Millisecond},
		{Scenario: "s<b>", Step: "a", Err: ErrTimeout, Latency: 2 * time.Second},
	}
	steps := []StepRecord{{Scenario: "s<b>", Step: "a", Duration: 50 * time.Millisecond, Err: ErrExpect}}
	iters := []IterationRecord{{Scenario: "s<b>", Duration: 60 * time.Millisecond, Err: ErrExpect}}
	rep := BuildReport(reqs, steps, iters, 3*time.Second)
	rep.Open = open
	return rep
}

func TestWriteHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport(false).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{"<!doctype html>", "closed (Users)", "2xx", "5xx", "timeout", "expect_mismatch", "p95"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い", want)
		}
	}
	// シナリオ名はエスケープされる（HTML として解釈されない）
	if strings.Contains(out, "s<b>") || !strings.Contains(out, "s&lt;b&gt;") {
		t.Error("シナリオ名がエスケープされていない")
	}
	if strings.Contains(out, "corrected (from scheduled start)") {
		t.Error("Users モデルでは corrected を出さない")
	}
	if strings.Contains(out, "http://") || strings.Contains(out, "https://") {
		t.Error("外部への参照を含めない")
	}
}

func TestWriteHTMLOpen(t *testing.T) {
	var buf bytes.Buffer
	rep := sampleReport(true)
	rep.Dropped = 7
	if err := rep.WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"open (Rate)", "corrected (from scheduled start)", "dropped"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い", want)
		}
	}
}

func TestWriteHTMLEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := BuildReport(nil, nil, nil, 0).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "</html>") {
		t.Fatal("空のレポートでも完結した HTML になること")
	}
}

func TestWriteHTMLTimeline(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reqs, iters := seriesRecords(t0)
	var buf bytes.Buffer
	if err := BuildReport(reqs, nil, iters, 0).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Timeline (1s)", "<svg", "<polyline", "requests / s", "concurrency"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い", want)
		}
	}
	if strings.Contains(out, "http://") || strings.Contains(out, "https://") {
		t.Error("外部への参照を含めない")
	}
}

func TestWriteHTMLNoTimeline(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport(false).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Timeline") {
		t.Error("時系列が無いときはグラフを出さない")
	}
}

func TestWriteHTMLConnection(t *testing.T) {
	reqs := []Record{
		{Code: 200, Latency: 10 * time.Millisecond, Conn: ConnNew, DNS: time.Millisecond, Connect: 2 * time.Millisecond, TLS: 3 * time.Millisecond},
		{Code: 200, Latency: 10 * time.Millisecond, Conn: ConnReused},
	}
	rep := BuildReport(reqs, nil, nil, time.Second)
	var buf bytes.Buffer
	if err := rep.WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"connection reuse", "50.0% reused", "connection setup", "connect (tcp)", "<h3>tls</h3>"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("出力に %q が無い", want)
		}
	}
	var text bytes.Buffer
	rep.Print(&text)
	if !strings.Contains(text.String(), "conns:      new=1 reused=1 (50.0% reused)") {
		t.Errorf("text 出力: %s", text.String())
	}
}
