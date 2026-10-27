package jotaro

import (
	"encoding/csv"
	"errors"
	"io"
	"math/rand/v2"
	"sync/atomic"
)

// FeedMode は Feeder が行を取り出す順序。
type FeedMode int

const (
	// Circular は先頭から順に配り、末尾まで来たら先頭に戻る。
	Circular FeedMode = iota
	// Random は毎回ランダムに選ぶ。同じ行が続けて選ばれることもある。
	Random
	// Once は先頭から順に配り、使い切ったら以降は false を返す。
	// 1 アカウントを 1 回しか使えない、といったデータ向け。
	Once
)

// Feeder はユーザーごとに別のデータ（アカウント、入力値など）を配る。
// 全ユーザーから同時に Next を呼んでよい。
type Feeder[T any] struct {
	rows []T
	mode FeedMode
	next atomic.Uint64
}

// NewFeeder は rows を mode の順で配る Feeder を作る。
func NewFeeder[T any](rows []T, mode FeedMode) *Feeder[T] {
	return &Feeder[T]{rows: rows, mode: mode}
}

// Next は次の行を返す。行が無い、または Once で使い切ったときは false。
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

// Len は行数。
func (f *Feeder[T]) Len() int { return len(f.rows) }

// LoadCSV は、1 行目をヘッダーとする CSV を、列名をキーとする行に読み込む。
func LoadCSV(r io.Reader) ([]map[string]string, error) {
	cr := csv.NewReader(r)
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("jotaro: CSV が空（ヘッダー行が必要）")
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
			row[h] = rec[i] // 列数の不一致は csv.Reader がエラーにする
		}
		rows = append(rows, row)
	}
}
