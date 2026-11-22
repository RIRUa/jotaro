package record

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// FuzzReadRaw feeds arbitrary bytes to ReadRaw. It must never panic, it must not return more records than the
// input has bytes (memory stays proportional to the input), and what it reads must survive WriteRaw → ReadRaw.
func FuzzReadRaw(f *testing.F) {
	var valid bytes.Buffer
	if err := WriteRaw(&valid, sampleRaw()); err != nil {
		f.Fatal(err)
	}
	lines := strings.SplitAfter(valid.String(), "\n")
	f.Add(valid.Bytes())
	f.Add([]byte(strings.Join(lines[:3], "")))                     // meta and a request
	f.Add([]byte(strings.Join(lines[:3], "") + lines[3][:10]))     // last line cut off
	f.Add([]byte(`{"type":"meta","version":1}` + "\n"))            // no records
	f.Add([]byte(`{"type":"meta","version":1}{"type":"request"}`)) // no newline between lines
	f.Add([]byte(`{"type":"future","x":1}` + "\n"))                // unknown type
	f.Add([]byte(`{"type":"meta","version":99}` + "\n"))
	f.Add([]byte(`{"type":"request","latency_ns":-1,"start":"0001-01-01T00:00:00Z"}` + "\n"))
	f.Add([]byte(`{"type":"request","latency_ns":9223372036854775807,"start":"9999-12-31T23:59:59Z"}` + "\n"))
	f.Add([]byte(`{"type":"health","second":-1,"goroutines":-1,"cpu":1e308}` + "\n"))
	f.Add([]byte(`{"version":2,"requests":{}}`)) // an aggregated result, not raw data
	f.Add([]byte(""))
	f.Add([]byte("null\n"))
	f.Add([]byte("[]\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := ReadRaw(bytes.NewReader(data))
		if err != nil {
			return
		}
		if n := len(raw.Requests) + len(raw.Steps) + len(raw.Iterations) + len(raw.Health); n > len(data) {
			t.Fatalf("read %d records from %d bytes", n, len(data))
		}
		var buf bytes.Buffer
		if err := WriteRaw(&buf, raw); err != nil {
			t.Skip() // for example a time outside the years JSON can write; not what this target checks
		}
		again, err := ReadRaw(&buf)
		if err != nil {
			t.Fatalf("WriteRaw output cannot be read back: %v\n%s", err, buf.String())
		}
		raw.Truncated = false // WriteRaw does not keep it
		if !reflect.DeepEqual(again, raw) {
			t.Fatalf("changed in round trip:\n got %+v\nwant %+v", again, raw)
		}
	})
}
