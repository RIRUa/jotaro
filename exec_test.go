package jotaro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// testExec は結果の Record を records に集める executor を返す。
func testExec(t *testing.T, baseURL string, client *http.Client) (*executor, *[]Record) {
	t.Helper()
	var (
		mu      sync.Mutex
		records []Record
	)
	if client == nil {
		client = &http.Client{}
	}
	e := &executor{
		ctx:     context.Background(),
		client:  client,
		baseURL: baseURL,
		tag:     Record{Scenario: "s", Step: "st", UserID: 7, Iteration: 3},
		sink: func(r Record) {
			mu.Lock()
			records = append(records, r)
			mu.Unlock()
		},
	}
	return e, &records
}

func TestDoRecordsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"abc"}`))
	}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, nil)
	var out struct{ Token string }
	if err := newRequest(e, "GET", "/login").Do().Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token != "abc" {
		t.Fatalf("token = %q", out.Token)
	}

	if len(*recs) != 1 {
		t.Fatalf("records = %d, want 1", len(*recs))
	}
	r := (*recs)[0]
	if r.Code != 200 || r.Err != ErrNone {
		t.Fatalf("Code=%d Err=%q", r.Code, r.Err)
	}
	if r.Scenario != "s" || r.Step != "st" || r.UserID != 7 || r.Iteration != 3 {
		t.Fatalf("tag が引き継がれていない: %+v", r)
	}
	if r.URL != srv.URL+"/login" || r.Method != "GET" {
		t.Fatalf("URL/Method = %q %q", r.Method, r.URL)
	}
	if r.Latency <= 0 || r.TTFB <= 0 || r.TTFB > r.Latency {
		t.Fatalf("Latency=%v TTFB=%v", r.Latency, r.TTFB)
	}
	if r.BytesIn != int64(len(`{"token":"abc"}`)) {
		t.Fatalf("BytesIn = %d", r.BytesIn)
	}
	if !r.Scheduled.Equal(r.Start) {
		t.Fatalf("Users モデルでは Scheduled == Start のはず")
	}
}

func TestDoSendsJSONAndBearer(t *testing.T) {
	var gotAuth, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, nil)
	err := newRequest(e, "POST", "/orders").
		Bearer("tok").JSON(map[string]any{"item": "x"}).Do().ExpectStatus(201)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotType != "application/json" || gotBody["item"] != "x" {
		t.Fatalf("auth=%q type=%q body=%v", gotAuth, gotType, gotBody)
	}
	if (*recs)[0].BytesOut != int64(len(`{"item":"x"}`)) {
		t.Fatalf("BytesOut = %d", (*recs)[0].BytesOut)
	}
}

func TestHTTPErrorStatusIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, nil)
	resp := newRequest(e, "GET", "/nope").Do()
	if resp.Err() != nil || resp.Code != 404 {
		t.Fatalf("Err=%v Code=%d", resp.Err(), resp.Code)
	}
	if r := (*recs)[0]; r.Code != 404 || r.Err != ErrNone {
		t.Fatalf("Code=%d Err=%q", r.Code, r.Err)
	}

	err := resp.ExpectStatus(200)
	var ee *ExpectError
	if !errors.As(err, &ee) || ee.Want != 200 || ee.Got != 404 {
		t.Fatalf("err = %v", err)
	}
	if classify(err) != ErrExpect {
		t.Fatalf("classify = %q", classify(err))
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	e, recs := testExec(t, url, nil)
	resp := newRequest(e, "GET", "/").Do()
	if resp.Err() == nil {
		t.Fatal("エラーが返るはず")
	}
	r := (*recs)[0]
	if r.Err != ErrConnRefused || r.Code != 0 {
		t.Fatalf("Err=%q Code=%d", r.Err, r.Code)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, &http.Client{Timeout: 50 * time.Millisecond})
	newRequest(e, "GET", "/").Do()
	if r := (*recs)[0]; r.Err != ErrTimeout {
		t.Fatalf("Err = %q, want timeout", r.Err)
	}
}

func TestCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	e, recs := testExec(t, srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	newRequest(e, "GET", "/").Do()
	if r := (*recs)[0]; r.Err != ErrCanceled {
		t.Fatalf("Err = %q, want canceled", r.Err)
	}
}

func TestResolve(t *testing.T) {
	e := &executor{baseURL: "http://h:8080/"}
	cases := map[string]string{
		"/a":                  "http://h:8080/a",
		"a":                   "http://h:8080/a",
		"":                    "http://h:8080/",
		"https://other/x?y=1": "https://other/x?y=1",
		"/a?b=1":              "http://h:8080/a?b=1",
	}
	for in, want := range cases {
		if got := e.resolve(in); got != want {
			t.Errorf("resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONEncodeErrorIsNotRecorded(t *testing.T) {
	e, recs := testExec(t, "http://unused", nil)
	resp := newRequest(e, "POST", "/").JSON(make(chan int)).Do()
	if resp.Err() == nil {
		t.Fatal("エンコードできない値はエラーになるはず")
	}
	if len(*recs) != 0 {
		t.Fatalf("送信していないので記録は残らないはず: %d", len(*recs))
	}
}
