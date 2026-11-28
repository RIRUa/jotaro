package jotaro

import (
	"context"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
)

// trackedConn is a connection dialed by a run's transport. It remembers whether any request has been
// handed this connection yet, so the first request to get it can be told apart from the later ones.
//
// httptrace.GotConnInfo.Reused cannot do that: net/http reports every connection that passed through the
// idle pool as reused, including a freshly dialed one that was parked there because the request that
// started the dial got another connection first. Such a connection carried no request before, so the
// request that finally uses it is the one that opened it as far as the run is concerned.
type trackedConn struct {
	net.Conn
	used atomic.Bool
}

// firstUse marks the connection as used and reports whether this call was the first to do so.
// It is safe for concurrent use: with HTTP/2 several requests may get the same connection at once,
// and exactly one of them is the first.
func (c *trackedConn) firstUse() bool {
	return c.used.CompareAndSwap(false, true)
}

// trackDials makes tr hand out trackedConn values from the dial of plain connections (DialContext, or
// the deprecated Dial when only that is set). A transport that dials TLS itself (DialTLSContext or
// DialTLS) is left alone: net/http expects a *tls.Conn from those to read the handshake state and to
// pick HTTP/2, which a wrapper would hide. Its requests fall back to GotConnInfo.Reused.
func trackDials(tr *http.Transport) {
	dial := tr.DialContext
	switch {
	case dial != nil:
	case tr.Dial != nil: //nolint:staticcheck // keep honoring a Dial set by whoever configured http.DefaultTransport
		legacy := tr.Dial //nolint:staticcheck // see above
		dial = func(_ context.Context, network, addr string) (net.Conn, error) {
			return legacy(network, addr)
		}
	default:
		dial = (&net.Dialer{}).DialContext
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &trackedConn{Conn: c}, nil
	}
}

// connFirstUse reports whether the request that was handed info.Conn is the first to use it.
//
// The connection is found by unwrapping info.Conn (TLS and the like) down to the trackedConn that
// trackDials installed. If there is none, for example because the client does not use a transport of
// this run, it falls back to !info.Reused, which is exact for the plain pool but undercounts the dials
// that went through the idle pool (see trackedConn).
func connFirstUse(info httptrace.GotConnInfo) bool {
	c := info.Conn
	for c != nil {
		switch v := c.(type) {
		case *trackedConn:
			return v.firstUse()
		case interface{ NetConn() net.Conn }: // *tls.Conn
			c = v.NetConn()
		default:
			return !info.Reused
		}
	}
	return !info.Reused
}
