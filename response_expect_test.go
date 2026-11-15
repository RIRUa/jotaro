package jotaro

import (
	"errors"
	"net/http"
	"testing"
)

func newResp(code int, body string) *Response {
	return &Response{Code: code, Header: http.Header{"X-A": {"b"}}, Body: []byte(body)}
}

func TestExpectHelpers(t *testing.T) {
	r := newResp(201, `{"data":{"items":[{"id":"x1"},{"id":"x2"}],"n":3}}`)
	ok := []error{
		r.ExpectStatusIn(200, 201),
		r.ExpectHeader("X-A", "b"),
		r.ExpectBodyContains("x2"),
		r.ExpectJSON("data.items.1.id", "x2"),
		r.ExpectJSON("data.n", 3),
	}
	for i, err := range ok {
		if err != nil {
			t.Errorf("case %d: unexpected error %v", i, err)
		}
	}
	bad := []error{
		r.ExpectStatusIn(200),
		r.ExpectHeader("X-A", "c"),
		r.ExpectBodyContains("zzz"),
		r.ExpectJSON("data.items.0.id", "x2"),
		r.ExpectJSON("data.missing", 1),
		r.ExpectJSON("data.items.9", 1),
		newResp(200, "not json").ExpectJSON("a", 1),
	}
	for i, err := range bad {
		var ee *ExpectError
		if !errors.As(err, &ee) {
			t.Errorf("case %d: want *ExpectError, got %v", i, err)
		}
	}
}

func TestJSONExtract(t *testing.T) {
	r := newResp(200, `{"data":{"items":[{"id":"x1"}]}}`)
	v, err := r.JSON("data.items.0.id")
	if err != nil || v != "x1" {
		t.Fatalf("got %v, %v", v, err)
	}
	if _, err := r.JSON("data.items.a"); err == nil {
		t.Fatal("want error for non-numeric index")
	}
}
