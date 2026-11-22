package report

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RIRUa/jotaro/record"
)

// TestFromRawOldFormats aggregates the stored raw data of each release and compares it with the
// stored JSON result of the same run (both files of a release were written by one run). Counts,
// errors and the time series are the same; only p50 to p99 may differ (see FromRaw).
func TestFromRawOldFormats(t *testing.T) {
	for _, release := range []string{"v0.1.0", "v0.1.2", "v0.1.3"} {
		t.Run(release, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "record", "testdata", "raw-"+release+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := record.ReadRaw(bytes.NewReader(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
			if err != nil {
				t.Fatal(err)
			}
			got := FromRaw(raw)
			want := readFixture(t, "v3-"+release+".json")

			if got.Elapsed != want.Elapsed || got.Open != want.Open || got.Dropped != want.Dropped {
				t.Errorf("elapsed/open/dropped = %v/%v/%d, want %v/%v/%d", got.Elapsed, got.Open, got.Dropped, want.Elapsed, want.Open, want.Dropped)
			}
			gq, wq := got.Requests, want.Requests
			if gq.Count != wq.Count || !reflect.DeepEqual(gq.Status, wq.Status) || !reflect.DeepEqual(gq.Errors, wq.Errors) ||
				gq.NewConns != wq.NewConns || gq.ReusedConns != wq.ReusedConns || gq.BytesIn != wq.BytesIn || gq.BytesOut != wq.BytesOut {
				t.Errorf("requests differ:\n got %+v\nwant %+v", gq, wq)
			}
			if gq.Latency.Min != wq.Latency.Min || gq.Latency.Max != wq.Latency.Max {
				t.Errorf("latency min/max = %v/%v, want %v/%v", gq.Latency.Min, gq.Latency.Max, wq.Latency.Min, wq.Latency.Max)
			}
			if len(got.Scenarios) != 1 || len(want.Scenarios) != 1 {
				t.Fatalf("scenarios = %d and %d, want 1", len(got.Scenarios), len(want.Scenarios))
			}
			gs, ws := got.Scenarios[0], want.Scenarios[0]
			if gs.Name != ws.Name || gs.Iterations.Count != ws.Iterations.Count || gs.Iterations.Failed != ws.Iterations.Failed ||
				!reflect.DeepEqual(gs.Iterations.Errors, ws.Iterations.Errors) {
				t.Errorf("iterations differ:\n got %+v\nwant %+v", gs.Iterations, ws.Iterations)
			}
			if len(gs.Steps) != len(ws.Steps) {
				t.Fatalf("steps = %d, want %d", len(gs.Steps), len(ws.Steps))
			}
			for i := range ws.Steps {
				g, w := gs.Steps[i], ws.Steps[i]
				if g.Name != w.Name || g.Count != w.Count || g.Failed != w.Failed || !reflect.DeepEqual(g.Errors, w.Errors) ||
					g.Requests.Count != w.Requests.Count || !reflect.DeepEqual(g.Requests.Status, w.Requests.Status) {
					t.Errorf("step %d differs:\n got %+v\nwant %+v", i, g, w)
				}
			}
			if len(got.Series) != len(want.Series) {
				t.Fatalf("series points = %d, want %d", len(got.Series), len(want.Series))
			}
			for i := range want.Series {
				g, w := got.Series[i], want.Series[i]
				if g.Second != w.Second || g.Requests != w.Requests || g.Errors != w.Errors || g.HTTP5xx != w.HTTP5xx ||
					g.NewConns != w.NewConns || g.ReusedConns != w.ReusedConns || g.Concurrency != w.Concurrency {
					t.Errorf("series point %d differs:\n got %+v\nwant %+v", i, g, w)
				}
			}
			if !reflect.DeepEqual(got.Health, want.Health) {
				t.Errorf("health = %v, want %v", got.Health, want.Health)
			}
		})
	}
}
