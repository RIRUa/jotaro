package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RIRUa/jotaro/record"
)

func exportRaw() *record.Raw {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &record.Raw{
		StartedAt: t0,
		Requests: []record.Record{
			{Scenario: "s", Step: "b", Method: "GET", URL: "http://x/b", Code: 503, Start: t0.Add(1500 * time.Millisecond), Latency: 20 * time.Millisecond, BytesIn: 5},
			{Scenario: "s", Step: "a", Method: "POST", URL: "http://x/a", Code: 200, Start: t0, Latency: 10 * time.Millisecond, BytesOut: 3},
			{Scenario: "s", Step: "a", Method: "GET", URL: "http://x/a", Err: record.ErrTimeout, Start: t0.Add(100 * time.Millisecond), Latency: time.Second},
		},
	}
}

func TestWriteVegeta(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteVegeta(&buf, exportRaw()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	var got []vegetaResult
	for _, l := range lines {
		var v vegetaResult
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	// Ordered by start time, with sequential numbers.
	if got[0].Method != "POST" || got[1].Error != string(record.ErrTimeout) || got[2].Code != 503 {
		t.Errorf("unexpected order or content: %+v", got)
	}
	for i, v := range got {
		if v.Seq != uint64(i) {
			t.Errorf("seq[%d] = %d", i, v.Seq)
		}
	}
	if got[0].Attack != "s/a" || got[0].Latency != int64(10*time.Millisecond) || got[0].BytesOut != 3 {
		t.Errorf("first result = %+v", got[0])
	}
	if got[1].Code != 0 {
		t.Errorf("network error code = %d, want 0", got[1].Code)
	}
}

func TestWriteSeriesCSV(t *testing.T) {
	rep := FromRaw(exportRaw())
	var buf bytes.Buffer
	if err := rep.WriteSeriesCSV(&buf); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 { // header + two seconds
		t.Fatalf("got %d rows, want 3: %v", len(rows), rows)
	}
	if rows[0][0] != "second" || len(rows[1]) != len(rows[0]) {
		t.Errorf("bad header or width: %v", rows[:2])
	}
	if rows[1][1] != "2" || rows[2][1] != "1" {
		t.Errorf("requests per second = %s, %s; want 2, 1", rows[1][1], rows[2][1])
	}
	if rows[2][2] != "0" || rows[2][3] != "1" {
		t.Errorf("errors/http_5xx in second 1 = %s/%s; want 0/1", rows[2][2], rows[2][3])
	}
}
