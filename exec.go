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
	"syscall"
	"time"
)

// executor はリクエストを実行して Record を残す。
type executor struct {
	ctx     context.Context
	client  *http.Client
	baseURL string

	// tag は Scenario/Step/UserID/Iteration などを埋めた Record のひな形。
	tag Record
	// sched は予定到着時刻の置き場所。最初のリクエストが Scheduled に使って 0 に戻す。
	// ステップは順に実行されるので、ロックはしない。
	sched *time.Time
	sink  func(Record)
}

func (e *executor) resolve(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if path == "" {
		return e.baseURL
	}
	return strings.TrimRight(e.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func (e *executor) do(r *Request) *Response {
	if r.err != nil {
		return &Response{err: r.err}
	}

	url := e.resolve(r.Spec.Path)
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
	e.sink(rec)
	return resp
}

// tracer は httptrace のイベントから各区間の時間を求める。
// イベントは別 goroutine から呼ばれることがあるので mutex で守る。
type tracer struct {
	start time.Time

	mu               sync.Mutex
	dnsStart, dnsEnd time.Time
	conStart, conEnd time.Time
	tlsStart, tlsEnd time.Time
	firstByte        time.Time
	conn             ConnKind
}

func (t *tracer) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { t.set(&t.dnsStart, false) },
		DNSDone:  func(httptrace.DNSDoneInfo) { t.set(&t.dnsEnd, true) },
		// 複数アドレスへ順に接続する場合は、最初の開始から最後の完了までを測る。
		ConnectStart:      func(_, _ string) { t.set(&t.conStart, false) },
		ConnectDone:       func(_, _ string, _ error) { t.set(&t.conEnd, true) },
		TLSHandshakeStart: func() { t.set(&t.tlsStart, false) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { t.set(&t.tlsEnd, true) },
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.conn = ConnNew
			if info.Reused {
				t.conn = ConnReused
			}
		},
		GotFirstResponseByte: func() { t.set(&t.firstByte, true) },
	}
}

// set は overwrite が false なら最初の 1 回だけ記録する。
func (t *tracer) set(dst *time.Time, overwrite bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if overwrite || dst.IsZero() {
		*dst = time.Now()
	}
}

func (t *tracer) fill(rec *Record) {
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

// span は両端が揃っているときだけ区間の長さを返す。
func span(from, to time.Time) time.Duration {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return to.Sub(from)
}

// classify は通信エラーを ErrKind に分類する。
func classify(err error) ErrKind {
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
		return ErrNone
	case errors.As(err, &expect):
		return ErrExpect
	case errors.Is(err, context.Canceled):
		return ErrCanceled
	case errors.As(err, &dnsErr):
		return ErrDNS
	case errors.As(err, &certErr), errors.As(err, &recHdr),
		errors.As(err, &hostErr), errors.As(err, &authErr):
		return ErrTLS
	case errors.Is(err, syscall.ECONNREFUSED):
		return ErrConnRefused
	case errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return ErrTimeout
	default:
		return ErrOther
	}
}
