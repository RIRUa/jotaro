package jotaro

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"
)

// FuzzResponseJSON feeds arbitrary bodies and paths to Response.JSON and ExpectJSON. Neither may panic, and a value
// that JSON returns must be accepted by ExpectJSON at the same path.
func FuzzResponseJSON(f *testing.F) {
	body := `{"data":{"items":[{"id":7,"name":"tea"},{"id":8}],"n":3.5,"ok":true,"nil":null,"":{"x":1}},"0":"zero","a.b":1}`
	for _, path := range []string{
		"", "data", "data.items", "data.items.0.id", "data.items.1", "data.items.2", "data.items.-1", "data.items.01",
		"data.items.99999999999999999999", "data.n", "data.nil", "data.nil.x", "data.ok.x", "0", "a.b", "a..b", ".", "..",
		"data.", ".data", "data..x", "data.items.0.name.0", strings.Repeat("data.", 50),
	} {
		f.Add([]byte(body), path)
	}
	f.Add([]byte(`[[[[[[[[[[1]]]]]]]]]]`), "0.0.0.0.0.0.0.0.0.0")
	f.Add([]byte(`[1,2,3]`), "1")
	f.Add([]byte(`"text"`), "")
	f.Add([]byte(`{"n":12345678901234567890}`), "n")
	f.Add([]byte(`not json`), "a")
	f.Add([]byte(``), "")
	f.Add([]byte(`null`), "a")
	f.Fuzz(func(t *testing.T, body []byte, path string) {
		r := &Response{Code: 200, Body: body}
		got, err := r.JSON(path)
		if err != nil {
			_ = r.ExpectJSON(path, nil) // the error form of ExpectJSON must not panic either
			return
		}
		if err := r.ExpectJSON(path, got); err != nil {
			t.Fatalf("JSON(%q) = %v, but ExpectJSON with that value failed: %v", path, got, err)
		}
	})
}

// FuzzLoadCSV feeds arbitrary bytes to LoadCSV. It must never panic, and every row it returns must have exactly the
// keys of the header's non-empty column names.
func FuzzLoadCSV(f *testing.F) {
	for _, s := range []string{
		"email,name\na@x,A\nb@x,B\n",
		"email,name\n",
		"email,name",
		"\xef\xbb\xbfemail,name\na@x,A\n",
		"a,,b\n1,2,3\n",
		"a,a\n1,2\n",
		"a,b\n1\n",
		"a,b\n1,2,3\n",
		"a,b\n\"x,\ny\",\"say \"\"hi\"\"\"\n",
		"a,b\n\"unterminated,1\n",
		"a\r\n1\r\n2\r\n",
		"\n\n\na\n1\n",
		"",
		"\xef\xbb\xbf",
		",\n,\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rows, err := LoadCSV(bytes.NewReader(data))
		if err != nil {
			return
		}
		// Read the header on our own, the way LoadCSV promises to: skip a BOM, ignore empty names.
		hr := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), utf8BOM)))
		header, err := hr.Read()
		if err != nil && !errors.Is(err, csv.ErrFieldCount) {
			t.Fatalf("LoadCSV succeeded but the header cannot be read: %v", err)
		}
		want := map[string]bool{}
		for _, h := range header {
			if h != "" {
				want[h] = true
			}
		}
		for i, row := range rows {
			if len(row) != len(want) {
				t.Fatalf("row %d has %d keys, want %d (%v): %v", i, len(row), len(want), header, row)
			}
			for k := range row {
				if !want[k] {
					t.Fatalf("row %d has the key %q, which is not in the header %v", i, k, header)
				}
			}
		}
	})
}

// fuzzStage turns fuzz values into a stage with a Rate or Ramp arrival that validates, or reports false.
func fuzzStage(kind bool, a, b int, per, over int64) (Stage, bool) {
	// Keep the sums of the stages far from overflow; a stage longer than a day is not what this checks.
	const day = int64(24 * time.Hour)
	per, over = per%day, over%day
	var arr Arrival
	if kind {
		arr = Rate(a%1000, time.Duration(per))
	} else {
		arr = Ramp(a%1000, b%1000, time.Duration(per), time.Duration(over))
	}
	if arr.validate() != nil {
		return Stage{}, false
	}
	return Stage{Arrival: arr, For: time.Duration(over)}, over > 0
}

// FuzzStagesAt checks that the arrival times of Stages never go backwards and stay inside their own stage:
// a stage's arrivals are in [its start, its start + For), and the stage counts all of them.
func FuzzStagesAt(f *testing.F) {
	s := int64(time.Second)
	f.Add(true, 50, 0, s, 30*s, false, 50, 0, s, 60*s, false, 50, 0, s, 10*s)
	f.Add(false, 0, 50, s, 30*s, true, 50, 0, s, 60*s, false, 50, 0, s, 10*s)
	f.Add(false, 11, 0, s, 10*s, false, 0, 11, s, 10*s, true, 1, 0, s, s)
	// Rate(999, 999ns) for just under a day has more arrivals than Stage.count looks for, so the count is cut at the cap.
	f.Add(true, 999, 0, int64(999), int64(86399999999999), true, 0, 0, int64(0), int64(0), true, 0, 0, int64(0), int64(0))
	f.Add(true, 1, 0, s, 3*s, true, 1000, 0, int64(time.Millisecond), s, true, 0, 0, s, s)
	f.Fuzz(func(t *testing.T, k1 bool, a1, b1 int, p1, o1 int64, k2 bool, a2, b2 int, p2, o2 int64, k3 bool, a3, b3 int, p3, o3 int64) {
		var stages []Stage
		for _, in := range []struct {
			kind      bool
			a, b      int
			per, over int64
		}{{k1, a1, b1, p1, o1}, {k2, a2, b2, p2, o2}, {k3, a3, b3, p3, o3}} {
			if st, ok := fuzzStage(in.kind, in.a, in.b, in.per, in.over); ok {
				stages = append(stages, st)
			}
		}
		if len(stages) == 0 {
			t.Skip()
		}
		arr := Stages(stages...)
		if arr.validate() != nil {
			t.Skip()
		}
		sa := arr.(stagedArrival)
		const edge = 300 // check this many arrivals at the start and at the end of each stage
		prev, first := time.Duration(0), 0
		for k, st := range sa.stages {
			n := sa.counts[k]
			var idx []int
			for i := range min(n, edge) {
				idx = append(idx, i)
			}
			for i := max(n-edge, min(n, edge)); i < n; i++ {
				idx = append(idx, i)
			}
			for _, i := range idx {
				got, ok := arr.at(first + i)
				if !ok {
					t.Fatalf("stage %d: at(%d) = false, but the stage has %d arrivals", k+1, first+i, n)
				}
				if got < prev {
					t.Fatalf("stage %d: at(%d) = %v is before the previous arrival %v", k+1, first+i, got, prev)
				}
				if got < sa.starts[k] || got >= sa.starts[k]+st.For {
					t.Fatalf("stage %d: at(%d) = %v is outside [%v, %v)", k+1, first+i, got, sa.starts[k], sa.starts[k]+st.For)
				}
				prev = got
			}
			// The arrival after the last one counted must be past the end of the stage (or not exist),
			// or the count came up short and arrivals are lost.
			// A stage that reaches maxStageArrivals is cut there on purpose, so it is not checked.
			if off, ok := st.Arrival.at(n); n < maxStageArrivals && ok && off < st.For {
				t.Fatalf("stage %d: arrival %d is at %v, inside the stage (For %v), but the stage counts only %d arrivals", k+1, n, off, st.For, n)
			}
			first += n
		}
		if _, ok := arr.at(first); ok {
			t.Fatalf("at(%d) = true, past the last arrival of the last stage", first)
		}
	})
}

// FuzzUserChanges checks the layout of Plan.UserStages: every stage moves the user count one user at a time,
// its changes are in time order and inside the stage, and the count ends exactly at the Target of the stage
// at the moment the stage ends.
func FuzzUserChanges(f *testing.F) {
	s := int64(time.Second)
	f.Add(0, 4, s*8, 4, s, 0, s*3)
	f.Add(2, 4, 2*s, 4, s, 0, 4*s)
	f.Add(3, 3, s, 3, s, 3, s)
	f.Add(0, 1000, int64(1)<<50, 0, int64(1)<<50, 7, int64(1))
	f.Add(0, 999, int64(3), 999, int64(1), 998, int64(1))
	f.Fuzz(func(t *testing.T, start, t1 int, f1 int64, t2 int, f2 int64, t3 int, f3 int64) {
		const maxTarget = 2000 // keeps the number of changes small
		stages := []UserStage{
			{Target: t1 % maxTarget, For: time.Duration(f1)},
			{Target: t2 % maxTarget, For: time.Duration(f2)},
			{Target: t3 % maxTarget, For: time.Duration(f3)},
		}
		start %= maxTarget
		// Stages longer than 2^50 ns (about 13 days) make the sum of For overflow, which is not what this checks.
		for i := range stages {
			stages[i].For = max(stages[i].For, -stages[i].For) % (1 << 50)
		}
		if validateUserStages(start, stages) != nil {
			t.Skip()
		}
		changes := userChanges(start, stages)
		cur, off, at := start, time.Duration(0), 0
		for k, st := range stages {
			end := off + st.For
			n := max(st.Target-cur, cur-st.Target)
			if at+n > len(changes) {
				t.Fatalf("stage %d needs %d changes, but only %d are left", k+1, n, len(changes)-at)
			}
			prev := off
			for _, c := range changes[at : at+n] {
				if c.at < prev || c.at > end {
					t.Fatalf("stage %d [%v, %v]: change at %v (previous %v)", k+1, off, end, c.at, prev)
				}
				if d := c.count - cur; d != 1 && d != -1 {
					t.Fatalf("stage %d: count went from %d to %d", k+1, cur, c.count)
				}
				cur, prev = c.count, c.at
			}
			if cur != st.Target {
				t.Fatalf("stage %d: the count ends at %d, want Target %d", k+1, cur, st.Target)
			}
			if n > 0 && prev != end {
				t.Fatalf("stage %d: the last change is at %v, want the end of the stage %v", k+1, prev, end)
			}
			at += n
			off = end
		}
		if at != len(changes) {
			t.Fatalf("%d changes are left over after the last stage", len(changes)-at)
		}
	})
}
