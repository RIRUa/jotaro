// Command jotaro is a CLI that works with result files saved by the jotaro library.
//
// Subcommands:
//
//   - report: build a report in text, JSON or HTML from a saved result
//   - merge: combine results from several runs (for example several load generators) into one
//   - check: compare a result with thresholds and exit with code 1 if any fail, which suits CI
//
// Usage:
//
//	jotaro report [-raw] [-format html|text|json] [-o output] <result.json | ->
//	jotaro merge  [-raw] [-format html|text|json] [-o output] <result.json>...
//	jotaro check  [-raw] -t <threshold> [-t <threshold>]... <result.json | ->
//
// With -raw, the input is read as raw JSON Lines data (the output of Plan.Raw) and re-aggregated.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

// usage is the top-level help text, printed for -h and for usage errors.
const usage = `usage: jotaro <command> [flags]

commands:
  report   build a report from a saved JSON result
  merge    combine several JSON results into one
  check    check whether a result meets the thresholds (e.g. p95<200ms); exit code 1 if not
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
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "jotaro: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
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

	rep, err := readReport(fs.Arg(0), *raw, stdin)
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
		fmt.Fprintln(stderr, "usage: jotaro merge [-raw] [-format html|text|json] [-o output] <result.json>...")
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
		rep, err := readReport(path, *raw, stdin)
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

	rep, err := readReport(fs.Arg(0), *raw, stdin)
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

// readReport reads a result from path (stdin if "-"). With raw, it aggregates raw data.
func readReport(path string, raw bool, stdin io.Reader) (*report.Report, error) {
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
		r, err := record.ReadRaw(in)
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
