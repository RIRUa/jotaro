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

func healthReport(pts ...HealthPoint) *Report {
	rep := BuildReport([]Record{{Code: 200, Latency: time.Millisecond}}, nil, nil, time.Second)
	rep.Health = pts
	return rep
}

func TestSummarizeHealth(t *testing.T) {
	if _, ok := summarizeHealth(nil); ok {
		t.Error("点が無いのに ok")
	}
	h, _ := summarizeHealth([]HealthPoint{{0, 10, 0.2}, {1, 30, 0.6}, {2, 20, -1}})
	if !h.HasCPU || h.MaxCPU != 0.6 || h.MaxGoroutines != 30 || h.Hot() {
		t.Errorf("h = %+v", h)
	}
	if got := h.AvgCPU; got < 0.399 || got > 0.401 { // -1（不明）は平均に含めない
		t.Errorf("AvgCPU = %v, want 0.4", got)
	}
	if h, _ := summarizeHealth([]HealthPoint{{0, 1, 0.95}}); !h.Hot() {
		t.Errorf("0.95 は詰まっている疑い: %+v", h)
	}
	if h, _ := summarizeHealth([]HealthPoint{{0, 1, -1}}); h.HasCPU || h.Hot() || !strings.Contains(h.String(), "cpu n/a") {
		t.Errorf("CPU 不明: %+v %q", h, h)
	}
}

func TestPrintHealth(t *testing.T) {
	var buf bytes.Buffer
	healthReport(HealthPoint{0, 10, 0.2}, HealthPoint{1, 30, 0.5}).Print(&buf)
	if out := buf.String(); !strings.Contains(out, "loadgen:    cpu max 50.0% (avg 35.0%), goroutines max 30") || strings.Contains(out, "warning:") {
		t.Errorf("出力:\n%s", out)
	}

	buf.Reset()
	healthReport(HealthPoint{0, 10, 0.97}).Print(&buf)
	if out := buf.String(); !strings.Contains(out, "warning:    load generator CPU reached 97%") {
		t.Errorf("警告が無い:\n%s", out)
	}

	buf.Reset()
	healthReport().Print(&buf)
	if strings.Contains(buf.String(), "loadgen") {
		t.Error("記録が無いときは出さない")
	}
}

func TestWriteHTMLHealth(t *testing.T) {
	var buf bytes.Buffer
	if err := healthReport(HealthPoint{0, 10, 0.2}, HealthPoint{1, 30, 0.95}).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"load generator goroutines", "load generator CPU", "load generator</div>", "cpu max 95.0%", "⚠ load generator CPU reached 95%"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い", want)
		}
	}

	// CPU が取得できない環境では、CPU のグラフを出さない。
	buf.Reset()
	if err := healthReport(HealthPoint{0, 10, -1}).WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "load generator goroutines") || strings.Contains(out, "load generator CPU (") {
		t.Error("CPU 不明のときはゴルーチンのグラフだけ")
	}

	buf.Reset()
	if err := healthReport().WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "load generator") {
		t.Error("記録が無いときは出さない")
	}
}
