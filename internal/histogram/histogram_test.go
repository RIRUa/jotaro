package histogram

import (
	"math"
	"testing"
	"time"
)

func within(got, want time.Duration, rel float64) bool {
	return math.Abs(float64(got-want)) <= rel*float64(want)
}

func TestNewEmpty(t *testing.T) {
	if h := New(nil); h != nil {
		t.Errorf("New(nil) = %v, want nil", h)
	}
	if n := H(nil).Total(); n != 0 {
		t.Errorf("Total = %d", n)
	}
}

func TestPercentileWithinAlpha(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 1000; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	h := New(ds)
	if h.Total() != 1000 {
		t.Fatalf("Total = %d", h.Total())
	}
	for _, p := range []int{50, 90, 95, 99, 100} {
		want := time.Duration(p*10) * time.Millisecond // nearest-rank の厳密値
		if got := h.Percentile(p); !within(got, want, Alpha) {
			t.Errorf("p%d = %v, want %v ± %.0f%%", p, got, want, Alpha*100)
		}
	}
}

func TestSmallValuesShareBucketZero(t *testing.T) {
	h := New([]time.Duration{0, 1, -5})
	if len(h) != 1 || h[0] != 3 {
		t.Errorf("h = %v、1ns 以下はバケット 0 にまとまるはず", h)
	}
}

func TestMergeIsSumAndKeepsInputs(t *testing.T) {
	a := New([]time.Duration{time.Millisecond, time.Millisecond, time.Second})
	b := New([]time.Duration{time.Millisecond, time.Minute})
	got := Merge(a, b)

	if got.Total() != a.Total()+b.Total() {
		t.Errorf("Total = %d", got.Total())
	}
	for i, c := range got {
		if c != a[i]+b[i] {
			t.Errorf("バケット %d: %d, want %d", i, c, a[i]+b[i])
		}
	}
	if a.Total() != 3 || b.Total() != 2 {
		t.Error("Merge が引数を書き換えた")
	}
	// 順序によらず同じ。
	if m := Merge(b, a); m.Total() != got.Total() || len(m) != len(got) {
		t.Error("Merge の結果が順序で変わった")
	}
	// 片方が nil でもよい。
	if Merge(nil, a).Total() != a.Total() {
		t.Error("nil との結合")
	}
}
