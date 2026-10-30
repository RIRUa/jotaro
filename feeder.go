package jotaro

import (
	"encoding/csv"
	"errors"
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

// LoadCSV reads a CSV whose first line is the header into rows keyed by column name.
func LoadCSV(r io.Reader) ([]map[string]string, error) {
	cr := csv.NewReader(r)
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("jotaro: CSV is empty (a header line is required)")
	}
	if err != nil {
		return nil, err
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
			row[h] = rec[i] // csv.Reader reports a column-count mismatch as an error
		}
		rows = append(rows, row)
	}
}
