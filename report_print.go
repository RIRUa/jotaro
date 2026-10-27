package jotaro

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// Print はレポートを人が読める形で w に書き出す。
func (r *Report) Print(w io.Writer) {
	fmt.Fprintln(w, "=== Summary ===")
	fmt.Fprintf(w, "elapsed:    %s\n", fmtDur(r.Elapsed))
	fmt.Fprintf(w, "requests:   %d (%.2f/s)\n", r.Requests.Count, r.RPS())
	fmt.Fprintf(w, "status:     %s\n", fmtStatus(r.Requests.Status))
	fmt.Fprintf(w, "errors:     %s\n", fmtErrors(r.Requests.Errors))
	fmt.Fprintf(w, "latency:    %s\n", fmtPercentiles(r.Requests.Latency))
	if r.Open {
		fmt.Fprintf(w, "corrected:  %s (from scheduled start)\n", fmtPercentiles(r.Requests.Corrected))
		fmt.Fprintf(w, "dropped:    %d\n", r.Dropped)
	}
	fmt.Fprintf(w, "ttfb:       %s\n", fmtPercentiles(r.Requests.TTFB))
	if q := r.Requests; q.NewConns+q.ReusedConns > 0 {
		fmt.Fprintf(w, "conns:      new=%d reused=%d (%.1f%% reused)\n", q.NewConns, q.ReusedConns, q.ReuseRate()*100)
		fmt.Fprintf(w, "dns:        %s\n", fmtPercentiles(q.DNS))
		fmt.Fprintf(w, "connect:    %s\n", fmtPercentiles(q.Connect))
		fmt.Fprintf(w, "tls:        %s\n", fmtPercentiles(q.TLS))
	}
	fmt.Fprintf(w, "bytes:      in %s, out %s\n", fmtBytes(r.Requests.BytesIn), fmtBytes(r.Requests.BytesOut))

	for _, sc := range r.Scenarios {
		fmt.Fprintf(w, "\n=== Scenario: %s ===\n", sc.Name)
		it := sc.Iterations
		fmt.Fprintf(w, "iterations: %d (%.2f/s), failed %d\n",
			it.Count, perSecond(it.Count, r.Elapsed), it.Failed)
		fmt.Fprintf(w, "errors:     %s\n\n", fmtErrors(it.Errors))

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "\tmin\tmean\tp50\tp90\tp95\tp99\tmax")
		writeLatencyRow(tw, "active (excl. think)", it.Active)
		writeLatencyRow(tw, "total", it.Total)
		tw.Flush()

		if len(sc.Steps) == 0 {
			continue
		}

		fmt.Fprintln(w, "\nsteps (duration):")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "step\tcount\tfail\tmin\tmean\tp50\tp90\tp95\tp99\tmax")
		for _, s := range sc.Steps {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", s.Name, s.Count, s.Failed, latencyCells(s.Duration))
		}
		tw.Flush()

		fmt.Fprintln(w, "\nsteps (requests):")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "step\treqs\tstatus\terrors\tp50\tp95\tp99\tttfb p50\tttfb p95")
		for _, s := range sc.Steps {
			q := s.Requests
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				s.Name, q.Count, fmtStatus(q.Status), fmtErrors(q.Errors),
				fmtDur(q.Latency.P50), fmtDur(q.Latency.P95), fmtDur(q.Latency.P99),
				fmtDur(q.TTFB.P50), fmtDur(q.TTFB.P95))
		}
		tw.Flush()
	}
}

func writeLatencyRow(w io.Writer, label string, l Latency) {
	fmt.Fprintf(w, "%s\t%s\n", label, latencyCells(l))
}

func latencyCells(l Latency) string {
	return strings.Join([]string{
		fmtDur(l.Min), fmtDur(l.Mean), fmtDur(l.P50), fmtDur(l.P90),
		fmtDur(l.P95), fmtDur(l.P99), fmtDur(l.Max),
	}, "\t")
}

func fmtPercentiles(l Latency) string {
	if l.Count == 0 {
		return "-"
	}
	return fmt.Sprintf("p50=%s p90=%s p95=%s p99=%s max=%s",
		fmtDur(l.P50), fmtDur(l.P90), fmtDur(l.P95), fmtDur(l.P99), fmtDur(l.Max))
}

func fmtStatus(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

func fmtErrors(m map[ErrKind]int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[ErrKind(k)])
	}
	return strings.Join(parts, " ")
}

func fmtDur(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(10 * time.Microsecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	f, suffix := float64(n), ""
	for _, s := range []string{"KB", "MB", "GB", "TB"} {
		f /= unit
		suffix = s
		if f < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f%s", f, suffix)
}
