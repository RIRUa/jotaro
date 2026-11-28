// Package memnet is an in-memory HTTP network for tests that run in a testing/synctest bubble.
//
// A real socket is not durably blocking, so a test with an httptest server cannot run on the fake clock of a
// bubble. net.Pipe connections are, so Start serves a handler over them: the client side (jotaro.Run, which
// clones http.DefaultTransport) and the handler both run inside the bubble, and every sleep in either of them
// costs no real time. It is for the tests of the examples, whose mock servers sleep to imitate latency.
// It is test support only: nothing in the library imports it.
package memnet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Host is the host name of URL. No address is resolved: whatever the client dials reaches the server.
const Host = "mem.test"

// Latency is the time every request takes before the handler runs, as on a real network. Without it a
// client whose requests fail at once (a server that answers 503 right away, a scenario with no think time)
// would loop without ever blocking, and the fake clock of a bubble moves only when every goroutine is blocked.
const Latency = 500 * time.Microsecond

// Server serves a handler on an in-memory network, and is what http.DefaultTransport dials until Close.
type Server struct {
	// URL is the base URL of the server (http://mem.test).
	URL string

	srv     *http.Server
	ln      *listener
	done    chan struct{}
	orig    http.RoundTripper
	closeMu sync.Once
}

// Start serves h and makes http.DefaultTransport an *http.Transport that dials it, whatever the address.
// Call it inside the bubble, and call Close before the bubble ends: Close stops the server's goroutines
// (a bubble waits for them) and puts back the transport that was replaced. Because it replaces a global,
// a test that uses it must not run in parallel with others that send requests through http.DefaultTransport.
func Start(h http.Handler) *Server {
	s := &Server{
		URL:  "http://" + Host,
		ln:   &listener{conns: make(chan net.Conn), closed: make(chan struct{})},
		done: make(chan struct{}),
		orig: http.DefaultTransport,
	}
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(Latency)
		h.ServeHTTP(w, r)
	})}
	go func() {
		defer close(s.done)
		_ = s.srv.Serve(s.ln) // always ErrServerClosed
	}()
	http.DefaultTransport = &http.Transport{DialContext: s.ln.dial}
	return s
}

// Close stops the server, closing its connections, waits for it to finish, and restores http.DefaultTransport.
func (s *Server) Close() {
	s.closeMu.Do(func() {
		http.DefaultTransport = s.orig
		_ = s.srv.Close()
		<-s.done
	})
}

// listener hands out the server ends of the pipes that dial creates.
type listener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *listener) Addr() net.Addr { return addr{} }

// dial connects a new pipe to the listener.
func (l *listener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		_ = client.Close()
		_ = server.Close()
		return nil, errors.New("memnet: server closed")
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	}
}

type addr struct{}

func (addr) Network() string { return "mem" }
func (addr) String() string  { return Host }
