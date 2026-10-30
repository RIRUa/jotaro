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
			t.Fatal("Circular should always return a value")
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
		t.Fatal("should be false once used up")
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
		t.Fatal("should be false when empty")
	}
}

// Even when called concurrently, Once hands out each row exactly once.
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
		t.Fatalf("rows handed out = %d, want %d", len(seen), n)
	}
	for v, c := range seen {
		if c != 1 {
			t.Fatalf("row %d was handed out %d times", v, c)
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
		t.Error("an empty CSV should be an error")
	}
	if _, err := LoadCSV(strings.NewReader("a,b\n1\n")); err == nil {
		t.Error("a CSV with mismatched columns should be an error")
	}
}
