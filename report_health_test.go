package jotaro

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHealthSampler(t *testing.T) {
	h := startHealthSampler(20 * time.Millisecond)

	// ゴルーチンを増やし、CPU も使う。
	stop := make(chan struct{})
	for range 20 {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}
	time.Sleep(110 * time.Millisecond)
	pts := h.stop()
	close(stop)

	if len(pts) < 3 {
		t.Fatalf("点が少ない: %+v", pts)
	}
	_, hasCPU := processCPUTime()
	for i, p := range pts {
		if p.Second != i {
			t.Errorf("pts[%d].Second = %d", i, p.Second)
		}
		if p.Goroutines < 20 {
			t.Errorf("pts[%d].Goroutines = %d、20 以上のはず", i, p.Goroutines)
		}
		switch {
		case !hasCPU && p.CPU != -1:
			t.Errorf("pts[%d].CPU = %v、取得できない環境では -1 のはず", i, p.CPU)
		case hasCPU && (p.CPU < 0 || p.CPU > 1):
			t.Errorf("pts[%d].CPU = %v、0〜1 に収まるはず", i, p.CPU)
		}
	}
	if last := pts[len(pts)-1]; hasCPU && last.CPU <= 0 {
		t.Errorf("busy loop を回しているのに CPU = %v", last.CPU)
	}
}

func TestHealthSamplerShortRun(t *testing.T) {
	h := startHealthSampler(time.Hour)
	time.Sleep(5 * time.Millisecond)
	// 間隔の半分に満たなくても、1 点も無いよりは最後に 1 点残す。
	if pts := h.stop(); len(pts) != 1 || pts[0].Second != 0 || pts[0].Goroutines < 1 {
		t.Errorf("pts = %+v", pts)
	}
}

func TestRunRecordsHealth(t *testing.T) {
	sc := NewScenario("s", func(ctx context.Context, u *User) error {
		u.Think(10 * time.Millisecond)
		return nil
	})
	rep, err := Run(context.Background(), Plan{Scenario: sc, Users: 2, Duration: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Health) == 0 {
		t.Fatal("Run の結果に Health が入っていない")
	}
	if p := rep.Health[0]; p.Second != 0 || p.Goroutines < 1 {
		t.Errorf("Health[0] = %+v", p)
	}
}

func TestHealthJSONRoundTrip(t *testing.T) {
	rep := BuildReport([]Record{{Latency: time.Millisecond}}, nil, nil, time.Second)
	rep.Health = []HealthPoint{{Second: 0, Goroutines: 12, CPU: 0.25}, {Second: 1, Goroutines: 40, CPU: -1}}

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Health, rep.Health) {
		t.Errorf("health differs:\n got %+v\nwant %+v", got.Health, rep.Health)
	}
}

func TestReadJSONWithoutHealth(t *testing.T) {
	rep, err := ReadJSON(strings.NewReader(`{"version":3,"requests":{},"scenarios":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Health != nil {
		t.Errorf("Health = %+v、無い JSON は nil として読む", rep.Health)
	}
}

func TestMergeHealth(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &Report{StartedAt: t0, Elapsed: 2 * time.Second, Health: []HealthPoint{{0, 10, 0.2}, {1, 12, 0.9}}}
	b := &Report{StartedAt: t0.Add(time.Second), Elapsed: 2 * time.Second, Health: []HealthPoint{{0, 5, 0.5}, {1, 6, -1}}}

	got, err := MergeReports(a, b)
	if err != nil {
		t.Fatal(err)
	}
	// b は 1 秒遅れて始まったので、b の 0 秒目は a の 1 秒目と重なる。
	want := []HealthPoint{{0, 10, 0.2}, {1, 12 + 5, 0.9}, {2, 6, -1}}
	if !reflect.DeepEqual(got.Health, want) {
		t.Errorf("Health = %+v\nwant     %+v", got.Health, want)
	}
}
