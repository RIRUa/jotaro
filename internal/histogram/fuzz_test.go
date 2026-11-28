package histogram

import (
	"encoding/binary"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

// fuzzDurations reads data as 8-byte values, each turned into a duration of 1ns or more.
// A bucket stands for a range of values, so 0 and 1 share bucket 0 and cannot be compared by relative error.
func fuzzDurations(data []byte) []time.Duration {
	var ds []time.Duration
	for ; len(data) >= 8; data = data[8:] {
		d := time.Duration(binary.LittleEndian.Uint64(data) & math.MaxInt64)
		ds = append(ds, max(d, 1))
	}
	return ds
}

func fuzzSeed(ds ...time.Duration) []byte {
	var b []byte
	for _, d := range ds {
		b = binary.LittleEndian.AppendUint64(b, uint64(d))
	}
	return b
}

// FuzzPercentile checks that Percentile is monotonic in p and within Alpha of the exact nearest-rank value,
// and that Merge is commutative and gives the histogram of all observations. The input is cut into two
// histograms at the byte given by split.
func FuzzPercentile(f *testing.F) {
	f.Add(fuzzSeed(time.Millisecond, 2*time.Millisecond, 3*time.Millisecond, time.Second), uint8(2))
	f.Add(fuzzSeed(1, 2, 3, 1000, 1000000, 1<<62), uint8(3))
	f.Add(fuzzSeed(5*time.Millisecond), uint8(0))
	f.Add(fuzzSeed(7, 7, 7, 7, 7, 7), uint8(1))
	f.Add(fuzzSeed(math.MaxInt64, math.MaxInt64-1), uint8(1))
	f.Add([]byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, split uint8) {
		ds := fuzzDurations(data)
		cut := int(split) % (len(ds) + 1)
		a, b, all := New(ds[:cut]), New(ds[cut:]), New(ds)

		ab, ba := Merge(a, b), Merge(b, a)
		if !reflect.DeepEqual(ab, ba) {
			t.Fatalf("Merge is not commutative: %v vs %v", ab, ba)
		}
		if len(ds) == 0 {
			return
		}
		if !reflect.DeepEqual(ab, all) {
			t.Fatalf("Merge(a, b) = %v, want the histogram of all observations %v", ab, all)
		}

		sorted := slices.Clone(ds)
		slices.Sort(sorted)
		n := len(sorted)
		prev := time.Duration(0)
		for p := 1; p <= 100; p++ {
			got := all.Percentile(p)
			if got < prev {
				t.Fatalf("Percentile(%d) = %v is below Percentile(%d) = %v", p, got, p-1, prev)
			}
			prev = got
			want := sorted[max((p*n+99)/100, 1)-1]
			// Alpha bounds the relative error; one nanosecond covers the rounding to a whole duration.
			if math.Abs(float64(got-want)) > Alpha*float64(want)+1 {
				t.Fatalf("Percentile(%d) = %v, exact nearest-rank value is %v (more than %v apart)", p, got, want, Alpha)
			}
		}
	})
}
