package jotaro

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWriteJSON(t *testing.T) {
	reqs := []Record{
		{Scenario: "s", Step: "a", Code: 200, Latency: 1500 * time.Nanosecond, TTFB: 700 * time.Nanosecond, BytesIn: 10, BytesOut: 2},
		{Scenario: "s", Step: "a", Err: ErrTimeout, Latency: 3 * time.Second},
	}
	steps := []StepRecord{
		{Scenario: "s", Step: "a", Duration: 2 * time.Second},
		{Scenario: "s", Step: "a", Duration: 3 * time.Second, Err: ErrTimeout},
	}
	iters := []IterationRecord{{Scenario: "s", Duration: 5 * time.Second, Think: time.Second}}
	rep := BuildReport(reqs, steps, iters, 10*time.Second)

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("JSON として読めない: %v\n%s", err, buf.String())
	}
	if got["version"] != float64(JSONVersion) {
		t.Fatalf("version = %v", got["version"])
	}
	if got["elapsed_ns"] != float64(10*time.Second) {
		t.Fatalf("elapsed_ns = %v", got["elapsed_ns"])
	}

	var typed jsonReport
	if err := json.Unmarshal(buf.Bytes(), &typed); err != nil {
		t.Fatal(err)
	}
	// ナノ秒の整数がそのまま出ること（丸めない）
	if typed.Requests.Latency.MinNS != 1500 {
		t.Fatalf("min_ns = %d, want 1500", typed.Requests.Latency.MinNS)
	}
	if typed.Requests.Errors["timeout"] != 1 || typed.Requests.Status["2xx"] != 1 {
		t.Fatalf("requests: %+v", typed.Requests)
	}
	sc := typed.Scenarios[0]
	if sc.Iterations.Active.MaxNS != int64(4*time.Second) || sc.Iterations.Total.MaxNS != int64(5*time.Second) {
		t.Fatalf("iterations: %+v", sc.Iterations)
	}
	if st := sc.Steps[0]; st.Failed != 1 || st.Errors["timeout"] != 1 {
		t.Fatalf("step: %+v", st)
	}
}

func TestWriteJSONEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := BuildReport(nil, nil, nil, 0).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// 空でも null ではなく []・{} になる（読む側が分岐しなくて済む）
	if s, ok := got["scenarios"].([]any); !ok || len(s) != 0 {
		t.Fatalf("scenarios = %v", got["scenarios"])
	}
	req := got["requests"].(map[string]any)
	if m, ok := req["errors"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("errors = %v", req["errors"])
	}
}

func TestReadJSONRoundTrip(t *testing.T) {
	for _, open := range []bool{false, true} {
		want := sampleReport(open)
		want.Dropped = 3

		var buf bytes.Buffer
		if err := want.WriteJSON(&buf); err != nil {
			t.Fatal(err)
		}
		got, err := ReadJSON(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("open=%v: 往復で変わった\ngot  %+v\nwant %+v", open, got, want)
		}
	}
}

func TestReadJSONEmptyRoundTrip(t *testing.T) {
	want := BuildReport(nil, nil, nil, 0)
	var buf bytes.Buffer
	if err := want.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReadJSONErrors(t *testing.T) {
	for name, in := range map[string]string{
		"壊れた JSON":     `{`,
		"version が無い":  `{}`,
		"未対応の version": `{"version": 999}`,
	} {
		if _, err := ReadJSON(strings.NewReader(in)); err == nil {
			t.Errorf("%s: error になること", name)
		}
	}
}

func TestReadJSONStartedAtAndBucketsRoundTrip(t *testing.T) {
	start := time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.UTC)
	want := BuildReport(
		[]Record{{Scenario: "s", Step: "a", Start: start, Code: 200, Latency: 3 * time.Millisecond}},
		nil, nil, time.Second)
	if !want.StartedAt.Equal(start) {
		t.Fatalf("StartedAt = %v, want %v", want.StartedAt, start)
	}
	var buf bytes.Buffer
	if err := want.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"started_at": "2026-05-06T07:08:09.123456789Z"`) || !strings.Contains(buf.String(), `"buckets"`) {
		t.Fatalf("started_at / buckets が出ていない:\n%s", buf.String())
	}
	got, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("往復で変わった\ngot  %+v\nwant %+v", got, want)
	}
}

func TestReadJSONVersion1(t *testing.T) {
	rep, err := ReadJSON(strings.NewReader(`{"version": 1, "elapsed_ns": 5}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Elapsed != 5 || !rep.StartedAt.IsZero() {
		t.Fatalf("%+v", rep)
	}
}
