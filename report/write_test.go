package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWriteFormats(t *testing.T) {
	rep := sampleReport(false)
	tests := []struct {
		f    Format
		want string
	}{
		{FormatText, "=== Summary ==="},
		{FormatJSON, `"version"`},
		{FormatHTML, "<!doctype html>"},
	}
	for _, tt := range tests {
		var buf bytes.Buffer
		if err := rep.Write(&buf, tt.f); err != nil {
			t.Fatalf("%s: %v", tt.f, err)
		}
		if !strings.Contains(buf.String(), tt.want) {
			t.Errorf("%s: %q missing from output", tt.f, tt.want)
		}
	}
}

func TestWriteUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport(false).Write(&buf, "xml"); err == nil {
		t.Fatal("an unknown format must be an error")
	}
	if buf.Len() != 0 {
		t.Fatal("nothing must be written for an unknown format")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteReturnsWriteError(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSON, FormatHTML} {
		if err := sampleReport(false).Write(failWriter{}, f); err == nil {
			t.Errorf("%s: a write failure must be returned", f)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"text", "json", "html"} {
		if f, err := ParseFormat(s); err != nil || string(f) != s {
			t.Errorf("ParseFormat(%q) = %q, %v", s, f, err)
		}
	}
	if _, err := ParseFormat("csv"); err == nil {
		t.Error("an unknown format must be an error")
	}
}
