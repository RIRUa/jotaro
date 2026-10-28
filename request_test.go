package jotaro

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
