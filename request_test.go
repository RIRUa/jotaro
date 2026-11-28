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
		t.Error("an unencodable value did not cause an error")
	}
}

func TestRequestHostHeader(t *testing.T) {
	var host, hdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, hdr = r.Host, r.Header.Get("X-Keep")
	}))
	defer srv.Close()

	cases := map[string]func(e *executor) *Request{
		"Header": func(e *executor) *Request { return newRequest(e, "GET", "/h").Header("Host", "tenant.example.com") },
		"Headers": func(e *executor) *Request {
			return newRequest(e, "GET", "/h").Headers(map[string]string{"Host": "tenant.example.com"})
		},
		"Host": func(e *executor) *Request { return newRequest(e, "GET", "/h").Host("tenant.example.com") },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			host, hdr = "", ""
			e, recs := testExec(t, srv.URL, nil)
			if err := build(e).Header("X-Keep", "1").Do().ExpectStatus(200); err != nil {
				t.Fatal(err)
			}
			if host != "tenant.example.com" || hdr != "1" {
				t.Errorf("host = %q, X-Keep = %q", host, hdr)
			}
			if len(*recs) != 1 || !strings.HasPrefix((*recs)[0].URL, srv.URL) {
				t.Errorf("records = %+v, want one request to %s", *recs, srv.URL)
			}
		})
	}

	// Without a Host header the host of the URL is sent, as before.
	e, _ := testExec(t, srv.URL, nil)
	if err := newRequest(e, "GET", "/h").Do().ExpectStatus(200); err != nil {
		t.Fatal(err)
	}
	if host != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("default host = %q", host)
	}
}

func TestRequestInvalidPath(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	for _, path := range []string{"/search?q=a b", "/search?q=a&x=b c#f", "/s?q=a\tb", "/s?q=a\x7fb"} {
		e, recs := testExec(t, srv.URL, nil)
		err := newRequest(e, "GET", path).Do().ExpectStatus(200)
		if err == nil || !strings.Contains(err.Error(), "url.QueryEscape") {
			t.Errorf("path %q: err = %v, want an error that points to url.QueryEscape", path, err)
		}
		if len(*recs) != 0 {
			t.Errorf("path %q: %d records, want none", path, len(*recs))
		}
	}
	if called {
		t.Error("the server was called for an invalid path")
	}

	// The path component is escaped by net/http, so a space or non-ASCII text in it is sent as %XX
	// (not an error), a fragment is never sent, and a query with existing %XX is sent as written.
	var uri string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { uri = r.RequestURI }))
	defer srv2.Close()
	e, _ := testExec(t, srv2.URL, nil)
	for path, want := range map[string]string{
		"/users/a b":                      "/users/a%20b",
		"/users/日本":                       "/users/%E6%97%A5%E6%9C%AC",
		"/users/" + url.PathEscape("a b"): "/users/a%20b",
		"/s?x=%E6%97%A5#f g":              "/s?x=%E6%97%A5",
	} {
		uri = ""
		if err := newRequest(e, "GET", path).Do().ExpectStatus(200); err != nil {
			t.Errorf("path %q: %v", path, err)
		}
		if uri != want {
			t.Errorf("path %q: RequestURI = %q, want %q", path, uri, want)
		}
	}
	if err := newRequest(e, "GET", "/s").Query("q", "a b").Do().ExpectStatus(200); err != nil || uri != "/s?q=a+b" {
		t.Errorf("Query: err = %v, RequestURI = %q", err, uri)
	}
}
