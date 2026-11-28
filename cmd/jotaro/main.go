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
//	jotaro help [command]
//
// Flags can come before or after the file names (jotaro report result.json -format text); "--" ends the flags.
// "jotaro help <command>" and "jotaro <command> -h" print the usage of the command to standard output.
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
       jotaro help <command>

commands:
  report   build a report from a saved JSON result
  merge    combine several JSON results into one
  check    check whether a result meets the thresholds (e.g. p95<200ms); exit code 1 if not
  compare  diff two results; with -max-p95-regression, exit code 1 on a regression
  export   convert raw data to vegeta results or a per-second CSV
  mcp      run an MCP server over stdio that helps write scenarios and read results
  version  print the version

Flags can come before or after the file names. "jotaro help <command>" shows the flags of a command.

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
		return runMCP(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		if len(args) > 1 && isHelpFlag(args[1]) {
			fmt.Fprintln(stdout, "usage: jotaro version\n  print the version")
			return 0
		}
		fmt.Fprintln(stdout, versionString())
		return 0
	case "help":
		if len(args) > 1 && !isHelpFlag(args[1]) {
			// "jotaro help report" is "jotaro report -h".
			return run([]string{args[1], "-h"}, stdin, stdout, stderr)
		}
		fmt.Fprint(stdout, usage)
		return 0
	case "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "jotaro: unknown command %q%s\n\n%s", args[0], suggestion(args[0]), usage)
		return 2
	}
}

// commandNames are the subcommands that "did you mean" suggests.
var commandNames = []string{"report", "merge", "compare", "export", "check", "mcp", "version", "help"}

// suggestion returns "; did you mean "check"?" for the command closest to name, or "" when none is close.
func suggestion(name string) string {
	best, bestDist := "", 3 // a distance of 3 or more is not a typo
	for _, c := range commandNames {
		if d := editDistance(name, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("; did you mean %q?", best)
}

// editDistance is the Levenshtein distance between a and b, counted in bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// isHelpFlag reports whether s asks for help.
func isHelpFlag(s string) bool {
	return s == "-h" || s == "-help" || s == "--help"
}

// newFlagSet makes the flag set of a subcommand. Its usage, the synopsis lines and the flag defaults, goes to stderr
// (it is shown because of an error); parseFlags prints it to standard output when help was asked for.
func newFlagSet(name string, stderr io.Writer, synopsis ...string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		for _, line := range synopsis {
			fmt.Fprintln(fs.Output(), line)
		}
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses args, which may mix flags and file names. It returns done = true with the exit code when the
// command must stop: 0 after printing the help that was asked for (to standard output), 2 for a bad flag.
func parseFlags(fs *flag.FlagSet, args []string, stdout io.Writer) (code int, done bool) {
	args, help := reorderArgs(fs, args)
	if help {
		fs.SetOutput(stdout)
		fs.Usage()
		return 0, true
	}
	if err := fs.Parse(args); err != nil {
		return 2, true
	}
	return 0, false
}

// usageError prints why the arguments are wrong, then the usage, to stderr, and returns the exit code 2.
func usageError(fs *flag.FlagSet, format string, a ...any) int {
	fmt.Fprintf(fs.Output(), "jotaro %s: %s\n", fs.Name(), fmt.Sprintf(format, a...))
	fs.Usage()
	return 2
}

// reorderArgs moves the flags of args, with their values, in front of the file names, so that
// "report result.json -format text" works although the flag package stops at the first file name.
// A lone "-" is a file name (standard input) and "--" ends the flags, as usual. help is true when
// -h, -help or --help is given as a flag (not as the value of another flag). When the last argument is a flag
// that needs a value, the file names are dropped and that flag comes last, so that Parse fails on it.
func reorderArgs(fs *flag.FlagSet, args []string) (reordered []string, help bool) {
	var flags, files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			files = append(files, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			files = append(files, a)
			continue
		}
		flags = append(flags, a)
		name, _, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			if isHelpFlag(a) {
				return nil, true
			}
			continue // unknown: Parse reports it
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); hasValue || (ok && b.IsBoolFlag()) {
			continue
		}
		if i+1 == len(args) {
			// A flag that needs a value is the last argument. Leave it last and add no "--" after it, so that
			// Parse reports "flag needs an argument" instead of taking the separator for the value.
			return flags, false
		}
		i++ // the next argument is the value, whatever it looks like
		flags = append(flags, args[i])
	}
	return append(flags, append([]string{"--"}, files...)...), false
}

// runExport implements "jotaro export". The input is always raw JSON Lines data (the output of Plan.Raw).
// Format "vegeta" writes one vegeta result per request, and "csv" writes the per-second time series.
// The exit code is 0 on success, 1 when reading or writing fails, 2 for bad usage.
func runExport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("export", stderr, "usage: jotaro export -format vegeta|csv [-o output] <raw.jsonl | ->")
	format := fs.String("format", "", "output format (vegeta, csv); required")
	out := fs.String("o", "", "output file; standard output if empty")
	if code, done := parseFlags(fs, args, stdout); done {
		return code
	}
	if code := checkFileCount(fs, 1, "one raw data file"); code != 0 {
		return code
	}
	var write func(io.Writer, *record.Raw) error
	switch *format {
	case "vegeta":
		write = report.WriteVegeta
	case "csv":
		write = func(w io.Writer, raw *record.Raw) error { return report.FromRaw(raw).WriteSeriesCSV(w) }
	case "":
		return usageError(fs, "no output format given (-format vegeta or -format csv)")
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
func runMCP(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("mcp", stderr,
		"usage: jotaro mcp",
		"  Serves MCP over stdin/stdout. Register it in an MCP client, e.g. claude mcp add jotaro -- jotaro mcp")
	if code, done := parseFlags(fs, args, stdout); done {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(fs, "mcp takes no arguments, got %q", fs.Arg(0))
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
	fs := newFlagSet("report", stderr, "usage: jotaro report [-raw] [-format html|text|json] [-o output] <result.json | ->")
	format := fs.String("format", "html", "output format (text, json, html)")
	out := fs.String("o", "", "output file; standard output if empty")
	raw := fs.Bool("raw", false, rawUsage)
	if code, done := parseFlags(fs, args, stdout); done {
		return code
	}
	if code := checkFileCount(fs, 1, "one result file"); code != 0 {
		return code
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
	fs := newFlagSet("merge", stderr, "usage: jotaro merge [-raw] [-format html|text|json] [-o output] <result.json | ->...")
	format := fs.String("format", "json", "output format (text, json, html)")
	out := fs.String("o", "", "output file; standard output if empty")
	raw := fs.Bool("raw", false, rawUsage)
	if code, done := parseFlags(fs, args, stdout); done {
		return code
	}
	if fs.NArg() < 1 {
		return usageError(fs, "no result file given")
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
	fs := newFlagSet("compare", stderr, "usage: jotaro compare [-raw] [-max-p95-regression <percent>] <base.json | -> <new.json | ->")
	gate := fs.Float64("max-p95-regression", -1, "exit with code 1 if any p95 got worse by more than this percentage (disabled if negative)")
	raw := fs.Bool("raw", false, rawUsage)
	if code, done := parseFlags(fs, args, stdout); done {
		return code
	}
	if code := checkFileCount(fs, 2, "two result files (the base and the new one)"); code != 0 {
		return code
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
	fs := newFlagSet("check", stderr,
		"usage: jotaro check [-raw] -t <threshold> [-t <threshold>]... <result.json | ->",
		"  e.g. jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json")
	var ts thresholdFlags
	fs.Var(&ts, "t", "threshold; can be repeated (e.g. p95<200ms)")
	raw := fs.Bool("raw", false, rawUsage)
	if code, done := parseFlags(fs, args, stdout); done {
		return code
	}
	if code := checkFileCount(fs, 1, "one result file"); code != 0 {
		return code
	}
	if len(ts) == 0 {
		return usageError(fs, "no threshold given (-t)")
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

// checkFileCount returns 0 when fs has exactly want file arguments. Otherwise it prints what is wrong and
// the usage, and returns the exit code 2. what describes the wanted files, for example "one result file".
func checkFileCount(fs *flag.FlagSet, want int, what string) int {
	switch got := fs.NArg(); got {
	case want:
		return 0
	case 0:
		return usageError(fs, "no input file given (want %s, or - for standard input)", what)
	default:
		return usageError(fs, "expected %s, got %d: %s", what, got, strings.Join(fs.Args(), " "))
	}
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
