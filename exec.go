package jotaro

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/RIRUa/jotaro/record"
)

// defaultMaxBodyBytes is the response body limit applied when Plan.MaxBodyBytes is 0.
const defaultMaxBodyBytes = 10 << 20

// ErrBodyTooLarge is returned (wrapped) when a response body is larger than Plan.MaxBodyBytes.
// The request is recorded as failed, and the body is not kept.
var ErrBodyTooLarge = errors.New("jotaro: response body exceeds Plan.MaxBodyBytes")

// executor runs requests and leaves a record.Record for each.
type executor struct {
	ctx     context.Context
	client  *http.Client
	baseURL string
	// maxBody is the largest response body to read. 0 means defaultMaxBodyBytes, negative means no limit.
	maxBody int64

	// tag is a template record.Record with Scenario, Step, UserID, Iteration and so on filled in.
	tag record.Record
	// sched holds the scheduled arrival time. The first request uses it for Scheduled and resets it to zero.
	// Steps run in order, so there is no locking.
	sched *time.Time
	sink  func(record.Record)
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

	url := withQuery(e.resolve(r.Spec.Path), r.Spec.Query)
	rec := e.tag
	rec.Method = r.Spec.Method
	rec.URL = url
	rec.BytesOut = int64(len(r.Spec.Body))

	rec.Start = time.Now()
	switch {
	case e.sched != nil && !e.sched.IsZero():
		rec.Scheduled = *e.sched
		*e.sched = time.Time{}
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

	resp := &Response{}
	hr, err := e.client.Do(httpReq)
	if err == nil {
		resp.Code = hr.StatusCode
		resp.Header = hr.Header
		rec.Code = hr.StatusCode
		resp.Body, err = e.readBody(hr.Body)
		hr.Body.Close()
	}
	rec.Latency = time.Since(rec.Start)
	rec.BytesIn = int64(len(resp.Body))
	tr.fill(&rec)

	if err != nil {
		rec.Err = classify(err)
		resp.err = err
	}
	e.sink(rec)
	return resp
}

// readBody reads the whole response body, failing with ErrBodyTooLarge instead of
// holding more than the limit in memory. The oversized body is discarded.
func (e *executor) readBody(r io.Reader) ([]byte, error) {
	limit := e.maxBody
	if limit < 0 {
		return io.ReadAll(r)
	}
	if limit == 0 {
		limit = defaultMaxBodyBytes
	}
	// Read one byte past the limit to tell "exactly at the limit" from "over it".
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(body)) > limit {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrBodyTooLarge, limit)
	}
	return body, err
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
}

// clientTrace returns the hooks to attach to the request context.
// Start events keep the first call only; done events keep the last.
func (t *tracer) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
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
			t.conn = record.ConnNew
			if info.Reused {
				t.conn = record.ConnReused
			}
		},
		GotFirstResponseByte: func() { t.set(&t.firstByte, true) },
	}
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
	var (
		expect  *ExpectError
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
		recHdr  tls.RecordHeaderError
		hostErr x509.HostnameError
		authErr x509.UnknownAuthorityError
		netErr  net.Error
	)
	switch {
	case err == nil:
		return record.ErrNone
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
