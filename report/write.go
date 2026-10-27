package report

import (
	"fmt"
	"io"
)

// Format はレポートの出力形式。
type Format string

const (
	FormatText Format = "text" // 人が読める表（Print と同じ）
	FormatJSON Format = "json" // 時間はナノ秒の整数
	FormatHTML Format = "html" // 外部に依存しない 1 枚の HTML
)

// ParseFormat は CLI のフラグなどの文字列を Format にする。
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case FormatText, FormatJSON, FormatHTML:
		return f, nil
	}
	return "", fmt.Errorf("jotaro: unknown format %q (text, json, html)", s)
}

// Write はレポートを f の形式で w に書き出す。
// 書き込みに失敗した場合は error を返す。途中まで書かれた内容は w に残る。
func (r *Report) Write(w io.Writer, f Format) error {
	switch f {
	case FormatText:
		ew := &errWriter{w: w}
		r.Print(ew)
		return ew.err
	case FormatJSON:
		return r.WriteJSON(w)
	case FormatHTML:
		return r.WriteHTML(w)
	}
	return fmt.Errorf("jotaro: unknown format %q", string(f))
}

// errWriter は最初の書き込みエラーを覚え、以降の書き込みを捨てる。
// エラーを返さない Print から、書き込みの失敗を取り出すために使う。
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}
