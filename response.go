package jotaro

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Response は 1 回のリクエストの結果。
// HTTP 4xx/5xx は通信エラーではないので Err は nil のまま、Code に入る。
type Response struct {
	Code   int
	Header http.Header
	Body   []byte

	err error
}

// ExpectError は ExpectStatus の期待値が外れたことを表す。
type ExpectError struct {
	Want int
	Got  int
}

func (e *ExpectError) Error() string {
	return fmt.Sprintf("jotaro: ステータスコードが期待と違う (want %d, got %d)", e.Want, e.Got)
}

// Err は通信エラーを返す。HTTP のステータスコードは見ない。
func (r *Response) Err() error {
	return r.err
}

// Decode はボディを JSON として v に読み込む。
func (r *Response) Decode(v any) error {
	if r.err != nil {
		return r.err
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("jotaro: レスポンスのデコードに失敗 (status %d): %w", r.Code, err)
	}
	return nil
}

// ExpectStatus はステータスコードが code と一致することを確認する。
func (r *Response) ExpectStatus(code int) error {
	if r.err != nil {
		return r.err
	}
	if r.Code != code {
		return &ExpectError{Want: code, Got: r.Code}
	}
	return nil
}
