package memnet

import (
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// A request through http.DefaultTransport reaches the handler, the handler's sleep and the network latency
// cost no real time, and Close puts the previous transport back.
func TestStartServesDefaultTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		orig := http.DefaultTransport
		srv := Start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(20 * time.Millisecond)
			io.WriteString(w, "hello "+r.URL.Path)
		}))
		defer srv.Close()
		if http.DefaultTransport == orig {
			t.Fatal("Start did not replace http.DefaultTransport")
		}

		begin := time.Now()
		for _, path := range []string{"/a", "/b"} { // the second request reuses the connection
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "hello "+path {
				t.Errorf("body = %q", body)
			}
		}
		if got, want := time.Since(begin), 2*(20*time.Millisecond+Latency); got != want {
			t.Errorf("two requests took %v on the fake clock, want %v", got, want)
		}

		srv.Close()
		if http.DefaultTransport != orig {
			t.Error("Close did not restore http.DefaultTransport")
		}
		srv.Close() // a second Close is harmless
	})
}

// A dial after Close fails instead of blocking.
func TestDialAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := Start(http.NotFoundHandler())
		tr := http.DefaultTransport.(*http.Transport)
		srv.Close()
		if _, err := tr.DialContext(t.Context(), "tcp", "mem.test:80"); err == nil {
			t.Error("dial after Close succeeded")
		}
	})
}
