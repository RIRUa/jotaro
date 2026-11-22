package jotaro

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
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
