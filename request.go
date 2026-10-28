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

// RequestSpec describes a request.
// It is kept as data separate from execution so that it can later be built from YAML and the like.
type RequestSpec struct {
	Method string
	Path   string
	Query  url.Values // added to the query of Path
	Header http.Header
	Body   []byte
}

// Request is a request builder. Send it with Do.
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

// JSON sets the body as JSON.
func (r *Request) JSON(v any) *Request {
	b, err := json.Marshal(v)
	if err != nil {
		r.err = fmt.Errorf("jotaro: failed to encode JSON: %w", err)
		return r
	}
	r.Spec.Body = b
	r.Spec.Header.Set("Content-Type", "application/json")
	return r
}

// Body sets the body according to its type.
//
//	[]byte / string  sent as is. Content-Type is not set; add it with Header if you need it
//	io.Reader        read to the end and sent as is. Content-Type is not set
//	url.Values       same as Form
//	anything else    same as JSON (structs, maps, slices, ...)
//
// nil sets nothing. Use JSON when you want to make it explicit that the body is JSON
// (also when you want to send a string as a JSON string).
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
			r.err = fmt.Errorf("jotaro: failed to read body: %w", err)
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

// Form sets the body as application/x-www-form-urlencoded.
func (r *Request) Form(v url.Values) *Request {
	r.Spec.Body = []byte(v.Encode())
	r.Spec.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// Query adds a query parameter. Repeating a key gives multiple values.
// Unlike writing it directly into Path, the value is escaped.
func (r *Request) Query(key, value string) *Request {
	if r.Spec.Query == nil {
		r.Spec.Query = url.Values{}
	}
	r.Spec.Query.Add(key, value)
	return r
}

// Queries adds several query parameters at once.
func (r *Request) Queries(v url.Values) *Request {
	for k, vs := range v {
		for _, x := range vs {
			r.Query(k, x)
		}
	}
	return r
}

// Headers sets several headers at once. An existing key is overwritten.
func (r *Request) Headers(h map[string]string) *Request {
	for k, v := range h {
		r.Header(k, v)
	}
	return r
}

// Header sets a header.
func (r *Request) Header(key, value string) *Request {
	r.Spec.Header.Set(key, value)
	return r
}

// Bearer sets a Bearer token in the Authorization header.
func (r *Request) Bearer(token string) *Request {
	return r.Header("Authorization", "Bearer "+token)
}

// Basic sets Basic authentication in the Authorization header.
func (r *Request) Basic(user, pass string) *Request {
	return r.Header("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
}

// Do sends the request.
// A network error is kept in the response and returned by Decode or ExpectStatus.
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
