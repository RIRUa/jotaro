// Command jotaro is a CLI that works with result files saved by the jotaro library.
//
// Subcommands:
//
//   - report: build a report in text, JSON or HTML from a saved result
//   - merge: combine results from several runs (for example several load generators) into one
//   - compare: diff two results, optionally failing when p95 regressed by more than a given percentage
//   - export: convert raw data to another tool's format (vegeta results, or a per-second CSV)
//   - check: compare a result with thresholds and exit with code 1 if any fail, which suits CI
//   - mcp: run an MCP server over stdio that helps an AI assistant write scenarios and read results
//   - version: print the version
//
// Usage:
//
//	jotaro report [-raw] [-format html|text|json] [-o output] <result.json | ->
//	jotaro merge  [-raw] [-format html|text|json] [-o output] <result.json | ->...
//	jotaro check  [-raw] -t <threshold> [-t <threshold>]... <result.json | ->
//	jotaro compare [-raw] [-max-p95-regression <percent>] <base.json | -> <new.json | ->
//	jotaro export -format vegeta|csv [-o output] <raw.jsonl | ->
//	jotaro mcp
//	jotaro version
//
// With -raw, the input is read as raw JSON Lines data (the output of Plan.Raw) and re-aggregated.
// "-" reads standard input, which can be consumed once, so at most one input of merge or compare can be "-".
// Raw data given where aggregated JSON is expected, and the reverse, is an error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/RIRUa/jotaro/internal/mcpserver"
	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// usage is the top-level help text, printed for -h and for usage errors.
const usage = `usage: jotaro <command> [flags]

commands:
  report   build a report from a saved JSON result
  merge    combine several JSON results into one
  check    check whether a result meets the thresholds (e.g. p95<200ms); exit code 1 if not
  compare  diff two results; with -max-p95-regression, exit code 1 on a regression
  export   convert raw data to vegeta results or a per-second CSV
  mcp      run an MCP server over stdio that helps write scenarios and read results
  version  print the version

report, merge, check and compare read a result file, or standard input when the file is "-"
(only one input can be "-", since standard input is read once). With -raw they read raw data,
the output of Plan.Raw. export reads raw data only.
`

// main only forwards to run, which holds the logic and can be tested.
func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run returns the exit code. It takes its input and output as arguments, so tests can call it.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "report":
		return runReport(args[1:], stdin, stdout, stderr)
	case "merge":
		return runMerge(args[1:], stdin, stdout, stderr)
	case "check":
		return runCheck(args[1:], stdin, stdout, stderr)
	case "compare":
		return runCompare(args[1:], stdin, stdout, stderr)
	case "export":
		return runExport(args[1:], stdin, stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, versionString())
		return 0
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "jotaro: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// runExport implements "jotaro export". The input is always raw JSON Lines data (the output of Plan.Raw).
// Format "vegeta" writes one vegeta result per request, and "csv" writes the per-second time series.
// The exit code is 0 on success, 1 when reading or writing fails, 2 for bad usage.
func runExport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro export -format vegeta|csv [-o output] <raw.jsonl | ->")
		fs.PrintDefaults()
	}
	format := fs.String("format", "", "output format (vegeta, csv); required")
	out := fs.String("o", "", "output file; standard output if empty")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	var write func(io.Writer, *record.Raw) error
	switch *format {
	case "vegeta":
		write = report.WriteVegeta
	case "csv":
		write = func(w io.Writer, raw *record.Raw) error { return report.FromRaw(raw).WriteSeriesCSV(w) }
	default:
		fmt.Fprintf(stderr, "jotaro: unknown export format %q (want vegeta or csv)\n", *format)
		return 2
	}

	in := stdin
	if path := fs.Arg(0); path != "-" {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, "jotaro:", err)
			return 1
		}
		defer file.Close()
		in = file
	}
	raw, err := readRawData(in, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "jotaro:", errMessage(err))
		return 1
	}

	if *out == "" {
		if err := write(stdout, raw); err != nil {
			fmt.Fprintln(stderr, "jotaro:", err)
			return 1
		}
		return 0
	}
	file, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(stderr, "jotaro:", err)
		return 1
	}
	if err := write(file, raw); err != nil {
		file.Close()
		_ = os.Remove(*out) // the write error comes first
		fmt.Fprintln(stderr, "jotaro:", err)
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(stderr, "jotaro:", err)
		return 1
	}
	return 0
}

// runMCP implements "jotaro mcp". It serves MCP over the process's standard input and output
// until the client disconnects or the process is interrupted, so it takes no input or output
// from the caller. The exit code is 0 on a normal stop, 1 on a server error, 2 for bad usage.
func runMCP(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro mcp")
		fmt.Fprintln(stderr, "  Serves MCP over stdin/stdout. Register it in an MCP client, e.g. claude mcp add jotaro -- jotaro mcp")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mcpserver.Run(ctx, versionNumber()); err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "jotaro:", err)
		return 1
	}
	return 0
}

// runReport implements "jotaro report". It returns the exit code:
// 0 on success, 1 when reading or writing fails, 2 for bad usage.
func runReport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro report [-raw] [-format html|text|json] [-o output] <result.json | ->")
		fs.PrintDefaults()
	}
	format := fs.String("format", "html", "output format (text, json, html)")
	out := fs.String("o", "", "output file; standard output if empty")
	raw := fs.Bool("raw", false, rawUsage)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	f, err := report.ParseFormat(*format)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	rep, err := readReport(fs.Arg(0), *raw, stdin, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "jotaro:", errMessage(err))
		return 1
	}

	return emit(rep, f, *out, stdout, stderr)
}

// emit writes the report to out (stdout if empty) and returns the exit code.
func emit(rep *report.Report, f report.Format, out string, stdout, stderr io.Writer) int {
	if out == "" {
		if err := rep.Write(stdout, f); err != nil {
			fmt.Fprintln(stderr, "jotaro:", err)
			return 1
		}
		return 0
	}
	if err := writeFile(out, rep, f); err != nil {
		fmt.Fprintln(stderr, "jotaro:", err)
		return 1
	}
	return 0
}

// runMerge implements "jotaro merge", with the same exit codes as runReport.
// Every input is read with the same -raw setting. The default output is JSON,
// so that the merged result can be merged or reported again.
func runMerge(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro merge [-raw] [-format html|text|json] [-o output] <result.json | ->...")
		fs.PrintDefaults()
	}
	format := fs.String("format", "json", "output format (text, json, html)")
	out := fs.String("o", "", "output file; standard output if empty")
	raw := fs.Bool("raw", false, rawUsage)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}

	f, err := report.ParseFormat(*format)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	reps := make([]*report.Report, 0, fs.NArg())
	for _, path := range fs.Args() {
		rep, err := readReport(path, *raw, stdin, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "jotaro: %s: %s\n", path, errMessage(err))
			return 1
		}
		reps = append(reps, rep)
	}
	merged, err := report.MergeReports(reps...)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(merged, f, *out, stdout, stderr)
}

// runCompare implements "jotaro compare". It prints per-step and overall diffs.
// With -max-p95-regression, it returns 1 when a p95 got worse by more than that percentage.
// Bad usage is 2, and unreadable input is 1.
func runCompare(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro compare [-raw] [-max-p95-regression <percent>] <base.json | -> <new.json | ->")
		fs.PrintDefaults()
	}
	gate := fs.Float64("max-p95-regression", -1, "exit with code 1 if any p95 got worse by more than this percentage (disabled if negative)")
	raw := fs.Bool("raw", false, rawUsage)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}

	var reps [2]*report.Report
	for i, path := range fs.Args() {
		rep, err := readReport(path, *raw, stdin, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "jotaro: %s: %s\n", path, errMessage(err))
			return 1
		}
		reps[i] = rep
	}

	cmp := report.Compare(reps[0], reps[1])
	cmp.Print(stdout)
	if *gate < 0 {
		return 0
	}
	bad := cmp.Violations(*gate)
	for _, d := range bad {
		fmt.Fprintf(stdout, "FAIL %s %s regressed by %.1f%% (limit %.1f%%)\n", d.Scope, d.Metric, d.Regression(), *gate)
	}
	if len(bad) > 0 {
		return 1
	}
	return 0
}

// thresholdFlags lets -t be given repeatedly.
type thresholdFlags []report.Threshold

// String is required by flag.Value. The value is never shown as a default.
func (t *thresholdFlags) String() string { return "" }

// Set parses one -t value and appends it. A bad expression fails flag parsing (exit code 2).
func (t *thresholdFlags) Set(s string) error {
	th, err := report.ParseThreshold(s)
	if err != nil {
		return err
	}
	*t = append(*t, th)
	return nil
}

// runCheck implements "jotaro check". It prints each check and returns 1 if any failed,
// so the exit code can gate a CI job. Bad usage is 2, and unreadable input is 1.
func runCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro check [-raw] -t <threshold> [-t <threshold>]... <result.json | ->")
		fmt.Fprintln(stderr, "  e.g. jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json")
		fs.PrintDefaults()
	}
	var ts thresholdFlags
	fs.Var(&ts, "t", "threshold; can be repeated (e.g. p95<200ms)")
	raw := fs.Bool("raw", false, rawUsage)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || len(ts) == 0 {
		fs.Usage()
		return 2
	}

	rep, err := readReport(fs.Arg(0), *raw, stdin, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "jotaro:", errMessage(err))
		return 1
	}

	checks := rep.Check(ts...)
	checks.Print(stdout)
	if !checks.Passed() {
		return 1
	}
	return 0
}

// errMessage strips the "jotaro: " prefix of library errors. The CLI adds its own, so it must not be doubled.
func errMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "jotaro: ")
}

// rawUsage is the shared help text of the -raw flag.
const rawUsage = "read the input as raw JSON Lines data and re-aggregate it"

// readRawData reads raw JSON Lines data. When the input is not raw data at all (an aggregated JSON result,
// or an empty file), the error says which input is wanted instead, so a mix-up is not silently accepted.
// When the last line is cut off (the run that wrote it was killed), the data is used and a warning goes to stderr.
func readRawData(in io.Reader, stderr io.Writer) (*record.Raw, error) {
	raw, err := record.ReadRaw(in)
	if errors.Is(err, record.ErrNotRaw) {
		return nil, fmt.Errorf("%s\nthe input is not raw data: pass the output of Plan.Raw. An aggregated JSON result (-format json) is not raw data; report, check, merge and compare read it without -raw", errMessage(err))
	}
	if err == nil && raw.Truncated {
		fmt.Fprintln(stderr, "jotaro: warning: the raw data ends in the middle of a line (the run was probably killed); the incomplete last line is ignored")
	}
	return raw, err
}

// readReport reads a result from path (stdin if "-"). With raw, it aggregates raw data.
func readReport(path string, raw bool, stdin io.Reader, stderr io.Writer) (*report.Report, error) {
	in := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		in = file
	}
	if raw {
		r, err := readRawData(in, stderr)
		if err != nil {
			return nil, err
		}
		return report.FromRaw(r), nil
	}
	return report.ReadJSON(in)
}

// writeFile removes the partly written file when writing fails.
func writeFile(path string, rep *report.Report, f report.Format) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := rep.Write(file, f); err != nil {
		file.Close()
		_ = os.Remove(path) // returning the write error comes first; failing to remove is not reported
		return err
	}
	return file.Close()
}
