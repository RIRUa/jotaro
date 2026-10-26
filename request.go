package jotaro

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// RequestSpec はリクエストの記述。
// 将来 YAML などから組み立てられるよう、実行とは切り離したデータとして持つ。
type RequestSpec struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// Request はリクエストのビルダー。Do で実行する。
type Request struct {
	Spec RequestSpec

	exec *executor
	err  error
}

func newRequest(exec *executor, method, path string) *Request {
	return &Request{
		Spec: RequestSpec{Method: method, Path: path, Header: http.Header{}},
		exec: exec,
	}
}

// JSON はボディを JSON として設定する。
func (r *Request) JSON(v any) *Request {
	b, err := json.Marshal(v)
	if err != nil {
		r.err = fmt.Errorf("jotaro: JSON エンコードに失敗: %w", err)
		return r
	}
	r.Spec.Body = b
	r.Spec.Header.Set("Content-Type", "application/json")
	return r
}

// Header はヘッダーを設定する。
func (r *Request) Header(key, value string) *Request {
	r.Spec.Header.Set(key, value)
	return r
}

// Bearer は Authorization ヘッダーに Bearer トークンを設定する。
func (r *Request) Bearer(token string) *Request {
	return r.Header("Authorization", "Bearer "+token)
}

// Do はリクエストを実行する。
// 通信エラーはレスポンスに保持され、Decode や ExpectStatus が返す。
func (r *Request) Do() *Response {
	return r.exec.do(r)
}
