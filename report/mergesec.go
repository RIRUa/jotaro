package report

import (
	"cmp"
	"slices"
)

// denseSlack bounds how sparse the seconds may be for mergeBySecond to index them with a plain slice instead of a map.
const denseSlack = 4

// mergeBySecond merges per-second points of several reports into one slice ordered by second.
//
// Point i of parts[k] lands on second second(p) + offsets[k]. The first point of a second is copied as is, and every
// later one is folded into it with add, in the order of parts and then of the points, so the result is the same
// as folding the reports one after another. Points of the same second inside one report are folded too.
// The time is linear in the number of points: a second is found through an index (a slice when the seconds are dense,
// as they are for a real run, and a map when they are not) and the sort is skipped when the output is already ordered,
// which it is unless an input is not sorted. It returns nil when there are no points.
func mergeBySecond[T any](parts [][]T, offsets []int, second func(T) int, setSecond func(*T, int), add func(dst *T, src T)) []T {
	total := 0
	lo, hi := 0, 0
	for k, part := range parts {
		for i := range part {
			s := second(part[i]) + offsets[k]
			if total == 0 {
				lo, hi = s, s
			} else {
				lo, hi = min(lo, s), max(hi, s)
			}
			total++
		}
	}
	if total == 0 {
		return nil
	}

	var (
		out   = make([]T, 0, total)
		dense []int // position+1 by second-lo, 0 if the second has no slot yet
		byMap map[int]int
	)
	// The span is computed in uint64 so that a corrupt, huge Second cannot overflow it.
	if span := uint64(hi) - uint64(lo) + 1; span != 0 && span <= uint64(denseSlack)*uint64(total)+64 {
		dense = make([]int, span)
	} else {
		byMap = make(map[int]int, total)
	}
	for k, part := range parts {
		for i := range part {
			p := part[i]
			s := second(p) + offsets[k]
			var pos int
			if dense != nil {
				pos = dense[s-lo] - 1
			} else if v, ok := byMap[s]; ok {
				pos = v
			} else {
				pos = -1
			}
			if pos < 0 {
				out = append(out, p)
				setSecond(&out[len(out)-1], s)
				if dense != nil {
					dense[s-lo] = len(out)
				} else {
					byMap[s] = len(out) - 1
				}
				continue
			}
			add(&out[pos], p)
		}
	}
	if !slices.IsSortedFunc(out, func(x, y T) int { return cmp.Compare(second(x), second(y)) }) {
		slices.SortFunc(out, func(x, y T) int { return cmp.Compare(second(x), second(y)) })
	}
	return out
}
