package jotaro

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RequestSpec はリクエストの記述。
// 将来 YAML などから組み立てられるよう、実行とは切り離したデータとして持つ。
type RequestSpec struct {
	Method string
	Path   string
	Query  url.Values // Path のクエリに足される
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

// Body はボディを型に応じて設定する。
//
//	[]byte / string  そのまま送る。Content-Type は設定しないので、必要なら Header で付ける
//	io.Reader        最後まで読んで、そのまま送る。Content-Type は設定しない
//	url.Values       Form と同じ
//	それ以外         JSON と同じ（構造体、map、スライスなど）
//
// nil は何も設定しない。JSON として送る意図をはっきりさせたいときは JSON を使う
// （string を JSON 文字列として送りたいときも JSON）。
func (r *Request) Body(v any) *Request {
	switch b := v.(type) {
	case nil:
		return r
	case []byte:
		r.Spec.Body = b
	case string:
		r.Spec.Body = []byte(b)
	case io.Reader:
		data, err := io.ReadAll(b)
		if err != nil {
			r.err = fmt.Errorf("jotaro: ボディの読み込みに失敗: %w", err)
			return r
		}
		r.Spec.Body = data
	case url.Values:
		return r.Form(b)
	default:
		return r.JSON(v)
	}
	return r
}

// Form はボディを application/x-www-form-urlencoded として設定する。
func (r *Request) Form(v url.Values) *Request {
	r.Spec.Body = []byte(v.Encode())
	r.Spec.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// Query はクエリパラメーターを足す。同じキーを繰り返すと複数の値になる。
// 値は Path に直書きするときと違い、エスケープされる。
func (r *Request) Query(key, value string) *Request {
	if r.Spec.Query == nil {
		r.Spec.Query = url.Values{}
	}
	r.Spec.Query.Add(key, value)
	return r
}

// Queries はクエリパラメーターをまとめて足す。
func (r *Request) Queries(v url.Values) *Request {
	for k, vs := range v {
		for _, x := range vs {
			r.Query(k, x)
		}
	}
	return r
}

// Headers はヘッダーをまとめて設定する。同じキーは上書きする。
func (r *Request) Headers(h map[string]string) *Request {
	for k, v := range h {
		r.Header(k, v)
	}
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

// Basic は Authorization ヘッダーに Basic 認証を設定する。
func (r *Request) Basic(user, pass string) *Request {
	return r.Header("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
}

// Do はリクエストを実行する。
// 通信エラーはレスポンスに保持され、Decode や ExpectStatus が返す。
func (r *Request) Do() *Response {
	return r.exec.do(r)
}

// withQuery は rawURL に q を足した URL を返す。rawURL にクエリがあれば、その後ろに足す。
func withQuery(rawURL string, q url.Values) string {
	if len(q) == 0 {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + q.Encode()
}
