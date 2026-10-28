package jotaro

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRequestSugar(t *testing.T) {
	var got struct {
		query, ctype, trace, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.query, got.ctype, got.trace, got.body = r.URL.RawQuery, r.Header.Get("Content-Type"), r.Header.Get("X-Trace"), string(b)
	}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, nil)
	req := newRequest(e, "POST", "/login?a=1").
		Query("q", "x y").
		Queries(url.Values{"tag": {"a", "b"}}).
		Headers(map[string]string{"X-Trace": "t1"}).
		Form(url.Values{"user": {"u"}, "pass": {"p&w"}})
	if err := req.Do().ExpectStatus(200); err != nil {
		t.Fatal(err)
	}

	q, _ := url.ParseQuery(got.query)
	if q.Get("a") != "1" || q.Get("q") != "x y" || len(q["tag"]) != 2 {
		t.Errorf("query = %q", got.query)
	}
	if got.ctype != "application/x-www-form-urlencoded" || got.trace != "t1" {
		t.Errorf("headers: ctype=%q trace=%q", got.ctype, got.trace)
	}
	if got.body != "pass=p%26w&user=u" {
		t.Errorf("body = %q", got.body)
	}
	if len(*recs) != 1 || (*recs)[0].BytesOut != int64(len(got.body)) {
		t.Errorf("record = %+v", *recs)
	}
}

func TestRequestBodyAndBasic(t *testing.T) {
	type rec struct{ ctype, auth, body string }
	var got rec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = rec{r.Header.Get("Content-Type"), r.Header.Get("Authorization"), string(b)}
	}))
	defer srv.Close()

	cases := []struct {
		name  string
		body  any
		ctype string
		want  string
	}{
		{"string", "a=1", "", "a=1"},
		{"bytes", []byte("raw"), "", "raw"},
		{"reader", strings.NewReader("rd"), "", "rd"},
		{"values", url.Values{"k": {"v"}}, "application/x-www-form-urlencoded", "k=v"},
		{"struct", struct{ N int }{1}, "application/json", `{"N":1}`},
		{"map", map[string]int{"a": 1}, "application/json", `{"a":1}`},
		{"nil", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := testExec(t, srv.URL, nil)
			if err := newRequest(e, "POST", "/x").Body(c.body).Do().ExpectStatus(200); err != nil {
				t.Fatal(err)
			}
			if got.ctype != c.ctype || got.body != c.want {
				t.Errorf("ctype=%q body=%q, want %q %q", got.ctype, got.body, c.ctype, c.want)
			}
		})
	}

	e, _ := testExec(t, srv.URL, nil)
	if err := newRequest(e, "GET", "/x").Basic("alice", "s3cret").Do().ExpectStatus(200); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Basic YWxpY2U6czNjcmV0" {
		t.Errorf("auth = %q", got.auth)
	}

	e, _ = testExec(t, srv.URL, nil)
	if err := newRequest(e, "POST", "/x").Body(make(chan int)).Do().ExpectStatus(200); err == nil {
		t.Error("JSON にできない値でエラーにならない")
	}
}
