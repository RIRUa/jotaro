package jotaro

import (
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestFeederCircular(t *testing.T) {
	f := NewFeeder([]int{1, 2, 3}, Circular)
	var got []int
	for range 7 {
		v, ok := f.Next()
		if !ok {
			t.Fatal("Circular は常に値を返すはず")
		}
		got = append(got, v)
	}
	if want := []int{1, 2, 3, 1, 2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFeederOnce(t *testing.T) {
	f := NewFeeder([]string{"a", "b"}, Once)
	for _, want := range []string{"a", "b"} {
		if v, ok := f.Next(); !ok || v != want {
			t.Fatalf("Next = %q, %v", v, ok)
		}
	}
	if _, ok := f.Next(); ok {
		t.Fatal("使い切ったら false のはず")
	}
}

func TestFeederRandomAndEmpty(t *testing.T) {
	rows := []int{1, 2, 3}
	f := NewFeeder(rows, Random)
	for range 50 {
		if v, ok := f.Next(); !ok || !slices.Contains(rows, v) {
			t.Fatalf("Next = %d, %v", v, ok)
		}
	}
	if _, ok := NewFeeder[int](nil, Circular).Next(); ok {
		t.Fatal("空なら false のはず")
	}
}

// Once は並行に呼んでも、各行がちょうど 1 回ずつ配られる。
func TestFeederOnceConcurrent(t *testing.T) {
	const n = 1000
	rows := make([]int, n)
	for i := range rows {
		rows[i] = i
	}
	f := NewFeeder(rows, Once)

	var (
		mu   sync.Mutex
		seen = map[int]int{}
		wg   sync.WaitGroup
	)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				v, ok := f.Next()
				if !ok {
					return
				}
				mu.Lock()
				seen[v]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("配られた行 = %d, want %d", len(seen), n)
	}
	for v, c := range seen {
		if c != 1 {
			t.Fatalf("行 %d が %d 回配られた", v, c)
		}
	}
}

func TestLoadCSV(t *testing.T) {
	rows, err := LoadCSV(strings.NewReader("user,pass\nalice,a1\nbob,\"b,2\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["user"] != "alice" || rows[1]["pass"] != "b,2" {
		t.Fatalf("rows = %v", rows)
	}
	if _, err := LoadCSV(strings.NewReader("")); err == nil {
		t.Error("空の CSV はエラーのはず")
	}
	if _, err := LoadCSV(strings.NewReader("a,b\n1\n")); err == nil {
		t.Error("列数が合わない CSV はエラーのはず")
	}
}
