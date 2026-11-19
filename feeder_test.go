package jotaro

import (
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestFeederCircular(t *testing.T) {
	f := NewFeeder([]int{1, 2, 3}, Circular)
	var got []int
	for range 7 {
		v, ok := f.Next()
		if !ok {
			t.Fatal("Circular should always return a value")
		}
		got = append(got, v)
	}
	if want := []int{1, 2, 3, 1, 2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFeederOnce(t *testing.T) {
	f := NewFeeder([]string{"a", "b"}, Once)
	for _, want := range []string{"a", "b"} {
		if v, ok := f.Next(); !ok || v != want {
			t.Fatalf("Next = %q, %v", v, ok)
		}
	}
	if _, ok := f.Next(); ok {
		t.Fatal("should be false once used up")
	}
}

func TestFeederRandomAndEmpty(t *testing.T) {
	rows := []int{1, 2, 3}
	f := NewFeeder(rows, Random)
	for range 50 {
		if v, ok := f.Next(); !ok || !slices.Contains(rows, v) {
			t.Fatalf("Next = %d, %v", v, ok)
		}
	}
	if _, ok := NewFeeder[int](nil, Circular).Next(); ok {
		t.Fatal("should be false when empty")
	}
}

// Even when called concurrently, Once hands out each row exactly once.
func TestFeederOnceConcurrent(t *testing.T) {
	const n = 1000
	rows := make([]int, n)
	for i := range rows {
		rows[i] = i
	}
	f := NewFeeder(rows, Once)

	var (
		mu   sync.Mutex
		seen = map[int]int{}
		wg   sync.WaitGroup
	)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				v, ok := f.Next()
				if !ok {
					return
				}
				mu.Lock()
				seen[v]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("rows handed out = %d, want %d", len(seen), n)
	}
	for v, c := range seen {
		if c != 1 {
			t.Fatalf("row %d was handed out %d times", v, c)
		}
	}
}

func TestLoadCSV(t *testing.T) {
	rows, err := LoadCSV(strings.NewReader("user,pass\nalice,a1\nbob,\"b,2\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["user"] != "alice" || rows[1]["pass"] != "b,2" {
		t.Fatalf("rows = %v", rows)
	}
	if _, err := LoadCSV(strings.NewReader("")); err == nil {
		t.Error("an empty CSV should be an error")
	}
	if _, err := LoadCSV(strings.NewReader("a,b\n1\n")); err == nil {
		t.Error("a CSV with mismatched columns should be an error")
	}
}

func TestLoadCSVStripsBOM(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	for name, in := range map[string]string{
		"plain":      bom + "email,password\na@x,p\n",
		"quoted":     bom + "\"email\",\"password\"\n\"a@x\",\"p\"\n",
		"crlf":       bom + "email,password\r\na@x,p\r\n",
		"single col": bom + "email\na@x\n",
	} {
		rows, err := LoadCSV(strings.NewReader(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(rows) != 1 || rows[0]["email"] != "a@x" {
			t.Errorf("%s: rows = %q, want the first column keyed %q", name, rows, "email")
		}
	}
	// Only a leading BOM is stripped: one in the middle of a field is data.
	rows, err := LoadCSV(strings.NewReader("a,b\n1," + bom + "2\n"))
	if err != nil || len(rows) != 1 || rows[0]["b"] != bom+"2" {
		t.Errorf("a BOM inside a field must be kept: rows = %q, err = %v", rows, err)
	}
	// Without a BOM nothing changes, including a header that starts with non-ASCII text.
	rows, err = LoadCSV(strings.NewReader("名前,pass\nalice,a1\n"))
	if err != nil || len(rows) != 1 || rows[0]["名前"] != "alice" {
		t.Errorf("rows = %q, err = %v", rows, err)
	}
}

func TestLoadCSVBOMOnly(t *testing.T) {
	// A file with only a BOM is an empty file, the same as "".
	rows, err := LoadCSV(strings.NewReader("\xef\xbb\xbf"))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("rows = %q, err = %v; want the empty-CSV error", rows, err)
	}
}

func TestLoadCSVBadHeader(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"duplicate", "a,a\n1,2\n", `"a"`},
		{"duplicate later", "id,name,id\n1,x,2\n", `"id"`},
		{"duplicate with BOM", "\xef\xbb\xbfa,a\n1,2\n", `"a"`},
		{"duplicate header only", "a,a\n", `"a"`},
		{"duplicate next to empty", "a,,a\n1,2,3\n", `"a"`},
	} {
		rows, err := LoadCSV(strings.NewReader(tc.in))
		if err == nil {
			t.Errorf("%s: rows = %q, want an error", tc.name, rows)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %q, want it to mention %s", tc.name, err, tc.want)
		}
	}
}

func TestLoadCSVHeaderOnly(t *testing.T) {
	// A header without rows is not an error: the Feeder is empty and Next returns false.
	rows, err := LoadCSV(strings.NewReader("a,b\n"))
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %q, err = %v", rows, err)
	}
	if _, ok := NewFeeder(rows, Once).Next(); ok {
		t.Error("Next on an empty Feeder should return false")
	}
}

func TestLoadCSVEmptyColumnName(t *testing.T) {
	for name, in := range map[string]string{
		"trailing comma":   "a,b,\n1,2,3\n",
		"middle":           "a,,b\n1,x,2\n",
		"several":          "a,,,b\n1,x,y,2\n",
		"BOM and trailing": "\xef\xbb\xbfa,b,\n1,2,\n",
	} {
		rows, err := LoadCSV(strings.NewReader(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(rows) != 1 || len(rows[0]) != 2 || rows[0]["a"] != "1" || rows[0]["b"] == "" {
			t.Errorf("%s: rows = %q, want only the columns a and b", name, rows)
		}
		if _, ok := rows[0][""]; ok {
			t.Errorf("%s: rows = %q, want no \"\" key", name, rows)
		}
	}
	// A row that is shorter than the header is still a column-count error.
	if _, err := LoadCSV(strings.NewReader("a,b,\n1,2\n")); err == nil {
		t.Error("a row with fewer fields than the header should be an error")
	}
}
