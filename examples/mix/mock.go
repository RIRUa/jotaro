package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// productCount is how many products the mock shop sells (ids 1 to productCount).
const productCount = 5

// shop is a small mock shop. Once broken is set, every request gets 503, like a server that fell over.
type shop struct {
	broken atomic.Bool
}

func (sh *shop) handler() http.Handler {
	delay := func(min, max time.Duration) { time.Sleep(min + rand.N(max-min)) }
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /products", func(w http.ResponseWriter, r *http.Request) {
		delay(2*time.Millisecond, 15*time.Millisecond)
		items := make([]map[string]any, productCount)
		for i := range items {
			items[i] = map[string]any{"id": i + 1, "name": fmt.Sprintf("item-%d", i+1), "price": 100 * (i + 1)}
		}
		reply(w, http.StatusOK, map[string]any{"currency": "JPY", "items": items})
	})
	mux.HandleFunc("GET /products/{id}", func(w http.ResponseWriter, r *http.Request) {
		delay(2*time.Millisecond, 10*time.Millisecond)
		reply(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "stock": rand.IntN(50)})
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		delay(5*time.Millisecond, 20*time.Millisecond)
		reply(w, http.StatusOK, map[string]string{"token": fmt.Sprintf("t-%d", rand.Int())})
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer t-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		delay(10*time.Millisecond, 50*time.Millisecond)
		if rand.IntN(100) < 10 {
			reply(w, http.StatusConflict, map[string]string{"error": "sold out"})
			return
		}
		reply(w, http.StatusCreated, map[string]string{"status": "accepted"})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sh.broken.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
