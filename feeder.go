package jotaro

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sync/atomic"
)

// FeedMode is the order in which a Feeder hands out rows.
type FeedMode int

const (
	// Circular hands out rows from the first one and wraps around after the last.
	Circular FeedMode = iota
	// Random picks a row at random each time. The same row can be picked twice in a row.
	Random
	// Once hands out rows from the first one and returns false once they are used up.
	// It is meant for data such as accounts that can be used only once.
	Once
)

// Feeder hands out different data (accounts, input values, ...) to each user.
// Next may be called from all users at the same time.
type Feeder[T any] struct {
	rows []T
	mode FeedMode
	// next is the shared cursor. Atomic, so concurrent users never get the same row in Circular or Once.
	next atomic.Uint64
}

// NewFeeder creates a Feeder that hands out rows in the order given by mode.
func NewFeeder[T any](rows []T, mode FeedMode) *Feeder[T] {
	return &Feeder[T]{rows: rows, mode: mode}
}

// Next returns the next row. It returns false when there are no rows or when Once has run out.
func (f *Feeder[T]) Next() (T, bool) {
	var zero T
	n := uint64(len(f.rows))
	if n == 0 {
		return zero, false
	}
	switch f.mode {
	case Random:
		return f.rows[rand.IntN(len(f.rows))], true
	case Once:
		i := f.next.Add(1) - 1
		if i >= n {
			return zero, false
		}
		return f.rows[i], true
	default:
		return f.rows[(f.next.Add(1)-1)%n], true
	}
}

// Len returns the number of rows.
func (f *Feeder[T]) Len() int { return len(f.rows) }

// utf8BOM is the byte order mark some tools write at the start of a UTF-8 file.
const utf8BOM = "\xef\xbb\xbf"

// LoadCSV reads a CSV whose first line is the header into rows keyed by column name.
//
// A leading UTF-8 byte order mark (as written by Excel's "CSV UTF-8" and many Windows tools)
// is skipped, so the first column is not keyed "\ufeffemail". A column with an empty
// name is ignored (a trailing comma in the header is common in spreadsheet exports), so
// its values are left out of the rows. A repeated non-empty column name is an error that names
// the column, because a map cannot hold both columns. Every row must have as many fields as the
// header. A file with a header and no rows is not an error: it returns no rows, and a Feeder
// made from them is empty, so Next returns false.
func LoadCSV(r io.Reader) ([]map[string]string, error) {
	br := bufio.NewReader(r)
	if b, err := br.Peek(len(utf8BOM)); err == nil && string(b) == utf8BOM {
		_, _ = br.Discard(len(utf8BOM))
	}
	cr := csv.NewReader(br)
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("jotaro: CSV is empty (a header line is required)")
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(header))
	for _, h := range header {
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			return nil, fmt.Errorf("jotaro: CSV header has the column %q more than once", h)
		}
		seen[h] = struct{}{}
	}
	var rows []map[string]string
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		row := make(map[string]string, len(header))
		for i, h := range header {
			if h == "" {
				continue
			}
			row[h] = rec[i] // csv.Reader reports a column-count mismatch as an error
		}
		rows = append(rows, row)
	}
}
