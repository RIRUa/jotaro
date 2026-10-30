package report

import (
	"fmt"
	"io"
)

// Format is the output format of a report.
type Format string

// The supported formats.
const (
	FormatText Format = "text" // a human-readable table (same as Print)
	FormatJSON Format = "json" // durations are integer nanoseconds
	FormatHTML Format = "html" // a single self-contained HTML file
)

// ParseFormat turns a string, such as a CLI flag, into a Format.
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case FormatText, FormatJSON, FormatHTML:
		return f, nil
	}
	return "", fmt.Errorf("jotaro: unknown format %q (text, json, html)", s)
}

// Write writes the report to w in format f.
// It returns an error if writing fails; what was written until then stays in w.
func (r *Report) Write(w io.Writer, f Format) error {
	switch f {
	case FormatText:
		ew := &errWriter{w: w}
		r.Print(ew)
		return ew.err
	case FormatJSON:
		return r.WriteJSON(w)
	case FormatHTML:
		return r.WriteHTML(w)
	}
	return fmt.Errorf("jotaro: unknown format %q", string(f))
}

// errWriter remembers the first write error and drops later writes.
// It is used to get write failures out of Print, which does not return errors.
type errWriter struct {
	w   io.Writer
	err error
}

// Write forwards to the underlying writer until the first error, which is then returned for every later call.
func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}
