// jotaro は結果ファイルを扱う CLI。
//
//	jotaro report [-format html|text|json] [-o 出力先] <result.json | ->
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/RIRUa/jotaro"
)

const usage = `usage: jotaro <command> [flags]

commands:
  report   保存した JSON の結果から、レポートを生成する
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
		fmt.Fprintln(stderr, "usage: jotaro report [-format html|text|json] [-o 出力先] <result.json | ->")
		fs.PrintDefaults()
	}
	format := fs.String("format", "html", "出力の形式 (text, json, html)")
	out := fs.String("o", "", "出力先のファイル。空なら標準出力")
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
	rep, err := jotaro.ReadJSON(in)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *out == "" {
		if err := rep.Write(stdout, f); err != nil {
			fmt.Fprintln(stderr, "jotaro:", err)
			return 1
		}
		return 0
	}
	if err := writeFile(*out, rep, f); err != nil {
		fmt.Fprintln(stderr, "jotaro:", err)
		return 1
	}
	return 0
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
