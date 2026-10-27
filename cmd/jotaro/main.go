// jotaro は結果ファイルを扱う CLI。
//
//	jotaro report [-raw] [-format html|text|json] [-o 出力先] <result.json | ->
//	jotaro merge  [-raw] [-format html|text|json] [-o 出力先] <result.json>...
//	jotaro check  [-raw] -t <しきい値> [-t <しきい値>]... <result.json | ->
//
// -raw を付けると、入力を JSON Lines の生データ（Plan.Raw の出力）として読み、集計し直す。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/record"
)

const usage = `usage: jotaro <command> [flags]

commands:
  report   保存した JSON の結果から、レポートを生成する
  merge    複数の JSON の結果を 1 つにまとめる
  check    結果がしきい値（例: p95<200ms）を満たすか確かめる。満たさなければ終了コード 1
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run は終了コードを返す。入出力を引数で受け取るので、テストから呼べる。
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

func runReport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro report [-raw] [-format html|text|json] [-o 出力先] <result.json | ->")
		fs.PrintDefaults()
	}
	format := fs.String("format", "html", "出力の形式 (text, json, html)")
	out := fs.String("o", "", "出力先のファイル。空なら標準出力")
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

	f, err := jotaro.ParseFormat(*format)
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

// emit はレポートを out（空なら stdout）に書き出し、終了コードを返す。
func emit(rep *jotaro.Report, f jotaro.Format, out string, stdout, stderr io.Writer) int {
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

func runMerge(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro merge [-raw] [-format html|text|json] [-o 出力先] <result.json>...")
		fs.PrintDefaults()
	}
	format := fs.String("format", "json", "出力の形式 (text, json, html)")
	out := fs.String("o", "", "出力先のファイル。空なら標準出力")
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

	f, err := jotaro.ParseFormat(*format)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	reps := make([]*jotaro.Report, 0, fs.NArg())
	for _, path := range fs.Args() {
		rep, err := readReport(path, *raw, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "jotaro: %s: %s\n", path, errMessage(err))
			return 1
		}
		reps = append(reps, rep)
	}
	merged, err := jotaro.MergeReports(reps...)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emit(merged, f, *out, stdout, stderr)
}

// thresholdFlags は -t を繰り返し指定できるようにする。
type thresholdFlags []jotaro.Threshold

func (t *thresholdFlags) String() string { return "" }

func (t *thresholdFlags) Set(s string) error {
	th, err := jotaro.ParseThreshold(s)
	if err != nil {
		return err
	}
	*t = append(*t, th)
	return nil
}

func runCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jotaro check [-raw] -t <しきい値> [-t <しきい値>]... <result.json | ->")
		fmt.Fprintln(stderr, "  例: jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json")
		fs.PrintDefaults()
	}
	var ts thresholdFlags
	fs.Var(&ts, "t", "しきい値。繰り返し指定できる（例: p95<200ms）")
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

// errMessage は、ライブラリのエラーが持つ "jotaro: " の接頭辞を外す。CLI が自分で付けるので二重にしない。
func errMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "jotaro: ")
}

const rawUsage = "入力を JSON Lines の生データとして読み、集計し直す"

// readReport は path（"-" なら stdin）から結果を読む。raw なら生データを集計する。
func readReport(path string, raw bool, stdin io.Reader) (*jotaro.Report, error) {
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
		return jotaro.ReportFromRaw(r), nil
	}
	return jotaro.ReadJSON(in)
}

// writeFile は書き込みに失敗したとき、途中まで書かれたファイルを消す。
func writeFile(path string, rep *jotaro.Report, f jotaro.Format) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := rep.Write(file, f); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	return file.Close()
}
