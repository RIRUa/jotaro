package jotaro

import (
	"context"
	"testing"
	"time"
)

func TestHealthSampler(t *testing.T) {
	h := startHealthSampler(20 * time.Millisecond)

	// ゴルーチンを増やし（待機させる）、CPU も使う（1 つだけ回す）。
	stop := make(chan struct{})
	for range 20 {
		go func() { <-stop }()
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	time.Sleep(150 * time.Millisecond)
	pts := h.stop()
	close(stop)

	if len(pts) < 2 {
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
