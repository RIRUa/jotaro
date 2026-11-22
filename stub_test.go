package jotaro

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// fakeReply is what a fakeServer answers to one request.
type fakeReply struct {
	// Delay is how long the "server" takes to answer. It is waited for on the clock of the caller, so inside
	// a synctest bubble it costs no real time. A request whose context ends first fails with the context error,
	// as a real transport does. Give it a positive value in any plan that loops (closed model): an instant
	// answer lets a user spin without ever blocking, and the fake clock then never moves.
	Delay time.Duration
	// Status is the HTTP status. 0 means 200.
	Status int
	// Body is the response body.
	Body string
}

// fakeServer is an in-memory http.RoundTripper: a server that answers every request after a delay,
// without sockets. Unlike an httptest server it needs no listener, adds no scheduling noise of its own, and
// works inside a synctest bubble, where real network I/O cannot (it is not durably blocking).
//
// Use it with synctest.Test and run(ctx, plan, stub), or set it as Client.Transport of a hand-made User.
// Anything that needs a real connection (httptrace phases, connection reuse, TLS, HTTP/2) must keep using
// httptest instead.
type fakeServer struct {
	// reply decides the answer to a request. Nil means 200 with an empty body and no delay.
	reply func(*http.Request) fakeReply
	// calls counts the requests that reached the stub.
	calls atomic.Int64
}

// fakeServerAfter is a fakeServer that answers every request after delay with 200.
func fakeServerAfter(delay time.Duration) *fakeServer {
	return &fakeServer{reply: func(*http.Request) fakeReply { return fakeReply{Delay: delay} }}
}

// fakeServerStatus is a fakeServer that answers every request after delay with the given status.
func fakeServerStatus(delay time.Duration, status int) *fakeServer {
	return &fakeServer{reply: func(*http.Request) fakeReply { return fakeReply{Delay: delay, Status: status} }}
}

// RoundTrip implements http.RoundTripper.
func (s *fakeServer) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	if req.Body != nil {
		defer req.Body.Close() // the RoundTripper contract: the transport closes the request body, even on error
	}
	var r fakeReply
	if s.reply != nil {
		r = s.reply(req)
	}
	if r.Delay > 0 {
		t := time.NewTimer(r.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	} else if err := req.Context().Err(); err != nil {
		return nil, err
	}
	status := r.Status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(r.Body)),
		ContentLength: int64(len(r.Body)),
		Request:       req,
	}, nil
}

// inBubble runs f in a synctest bubble: time.Now, timers, tickers and time.Sleep use a fake clock that moves
// forward only when every goroutine of the bubble is blocked, so a minute of load takes microseconds and every
// duration in f is exact. f must not use real network I/O, and every goroutine it starts must have exited
// when it returns (Run stops all of its own).
func inBubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	synctest.Test(t, f)
}
