package jotaro

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// executor runs requests and leaves a record.Record for each.
type executor struct {
	ctx     context.Context
	client  *http.Client
	baseURL string

	// tag is a template record.Record with Scenario, Step, UserID, Iteration and so on filled in.
	tag record.Record
	// sched holds the scheduled arrival time. The first request of the iteration takes it for Scheduled
	// and later ones get none. It may be nil. Requests of one step can run concurrently
	// (see Step), so the slot guards itself.
	sched *schedSlot
	// col receives the record of each request. When it is nil, sink does. col exists so that a step does not
	// allocate a method value of col.AddRequest for every executor.
	col  *record.Collector
	sink func(record.Record)
}

// schedSlot holds the scheduled arrival time of an iteration until a request takes it.
// It is safe for concurrent use: exactly one of several concurrent callers of take gets the time.
type schedSlot struct {
	mu sync.Mutex
	t  time.Time
}

// set stores the scheduled arrival time, replacing any that was not taken yet.
func (s *schedSlot) set(t time.Time) {
	s.mu.Lock()
	s.t = t
	s.mu.Unlock()
}

// take returns the stored time and clears it, so the next call returns the zero time.
func (s *schedSlot) take() time.Time {
	s.mu.Lock()
	t := s.t
	s.t = time.Time{}
	s.mu.Unlock()
	return t
}

// resolve turns a request path into a full URL.
// Absolute http(s) URLs pass through, an empty path means the base URL itself,
// and anything else is joined to the base URL with exactly one slash.
func (e *executor) resolve(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if path == "" {
		return e.baseURL
	}
	return strings.TrimRight(e.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// do sends r, reads the whole response body, and hands one record.Record to sink.
//
// It never returns nil: failures are carried in Response.err so that callers can
// chain Expect* checks without handling errors at every call.
// A request that was already invalid when built (r.err) is returned as is and records nothing,
// because nothing was sent.
func (e *executor) do(r *Request) *Response {
	if r.err != nil {
		return &Response{err: r.err}
	}

	if err := checkRawQuery(r.Spec.Path); err != nil {
		return &Response{err: err}
	}

	url := withQuery(e.resolve(r.Spec.Path), r.Spec.Query)
	rec := e.tag
	rec.Method = r.Spec.Method
	rec.URL = url
	rec.BytesOut = int64(len(r.Spec.Body))

	rec.Start = time.Now()
	var sched time.Time
	if e.sched != nil {
		sched = e.sched.take()
	}
	switch {
	case !sched.IsZero():
		rec.Scheduled = sched
	case rec.Scheduled.IsZero():
		rec.Scheduled = rec.Start
	}
	tr := &tracer{start: rec.Start}

	httpReq, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(e.ctx, tr.clientTrace()),
		r.Spec.Method, url, bytes.NewReader(r.Spec.Body),
	)
	if err != nil {
		return &Response{err: err}
	}
	httpReq.Header = r.Spec.Header.Clone()
	// net/http ignores a Host entry in Header; the value that goes out is Request.Host.
	if host := httpReq.Header.Get("Host"); host != "" {
		httpReq.Host = host
	}
	httpReq.Header.Del("Host")

	resp := &Response{}
	hr, err := e.client.Do(httpReq)
	if err == nil {
		resp.Code = hr.StatusCode
		resp.Header = hr.Header
		rec.Code = hr.StatusCode
		resp.Body, err = io.ReadAll(hr.Body)
		hr.Body.Close()
	}
	rec.Latency = time.Since(rec.Start)
	rec.BytesIn = int64(len(resp.Body))
	tr.fill(&rec)

	if err != nil {
		rec.Err = classify(err)
		resp.err = err
	}
	if e.col != nil {
		e.col.AddRequest(rec)
	} else {
		e.sink(rec)
	}
	return resp
}

// tracer derives the duration of each phase from httptrace events.
// Events can be called from other goroutines, so a mutex guards the fields.
type tracer struct {
	start time.Time

	mu               sync.Mutex
	dnsStart, dnsEnd time.Time
	conStart, conEnd time.Time
	tlsStart, tlsEnd time.Time
	firstByte        time.Time
	conn             record.ConnKind

	// trace is the set of hooks handed to net/http. It lives in the tracer, so one allocation serves both.
	trace httptrace.ClientTrace
}

// clientTrace returns the hooks to attach to the request context.
// Start events keep the first call only; done events keep the last.
// The returned value belongs to the tracer, and a second call rebuilds it in place, so call it once per tracer.
func (t *tracer) clientTrace() *httptrace.ClientTrace {
	t.trace = httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { t.set(&t.dnsStart, false) },
		DNSDone:  func(httptrace.DNSDoneInfo) { t.set(&t.dnsEnd, true) },
		// When connecting to several addresses in turn, measure from the first start to the last completion.
		ConnectStart:      func(_, _ string) { t.set(&t.conStart, false) },
		ConnectDone:       func(_, _ string, _ error) { t.set(&t.conEnd, true) },
		TLSHandshakeStart: func() { t.set(&t.tlsStart, false) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { t.set(&t.tlsEnd, true) },
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			// A request that got more than one connection (a retry after a stale one) keeps "new" if any
			// of them was its own, so no connection is counted as new more than once (a retry that was first to use two fresh connections counts as one new).
			if connFirstUse(info) {
				t.conn = record.ConnNew
			} else if t.conn == "" {
				t.conn = record.ConnReused
			}
		},
		GotFirstResponseByte: func() { t.set(&t.firstByte, true) },
	}
	return &t.trace
}

// set records only the first call when overwrite is false.
func (t *tracer) set(dst *time.Time, overwrite bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if overwrite || dst.IsZero() {
		*dst = time.Now()
	}
}

// fill copies the measured phases into rec. A phase that never happened
// (for example DNS on a reused connection) stays 0.
func (t *tracer) fill(rec *record.Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec.Conn = t.conn
	rec.DNS = span(t.dnsStart, t.dnsEnd)
	rec.Connect = span(t.conStart, t.conEnd)
	rec.TLS = span(t.tlsStart, t.tlsEnd)
	if !t.firstByte.IsZero() {
		rec.TTFB = t.firstByte.Sub(t.start)
	}
}

// span returns the length of the interval only when both ends are set.
func span(from, to time.Time) time.Duration {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return to.Sub(from)
}

// classify sorts a communication error into a record.ErrKind.
func classify(err error) record.ErrKind {
	// This check comes first on purpose. The variables below are targets of errors.As, so they are
	// moved to the heap when the function is entered, and a success (the common case: classify runs for
	// every step and iteration) would pay eight allocations for nothing.
	if err == nil {
		return record.ErrNone
	}
	var (
		pan     *panicError
		expect  *ExpectError
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
		recHdr  tls.RecordHeaderError
		hostErr x509.HostnameError
		authErr x509.UnknownAuthorityError
		netErr  net.Error
	)
	switch {
	case errors.As(err, &pan):
		return record.ErrPanic
	case errors.As(err, &expect):
		return record.ErrExpect
	case errors.Is(err, context.Canceled):
		return record.ErrCanceled
	case errors.As(err, &dnsErr):
		return record.ErrDNS
	case errors.As(err, &certErr), errors.As(err, &recHdr),
		errors.As(err, &hostErr), errors.As(err, &authErr):
		return record.ErrTLS
	case isConnRefused(err):
		return record.ErrConnRefused
	case errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return record.ErrTimeout
	default:
		return record.ErrOther
	}
}
