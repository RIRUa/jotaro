package jotaro

import (
	"errors"
	"net/http"
	"sync"
)

// Step is the entry point for building requests inside u.Step.
// Every request built from it is recorded under this step's name.
//
// A Step is safe for concurrent use: requests may be built and sent from several goroutines
// of the same step function, which is how a browser or mobile client that fetches a page and its API calls
// at once is modelled. Use [Step.Parallel] to do that without writing the goroutines and the waiting yourself.
// Every goroutine must have finished before the step function returns. In the open model the dispatch delay
// of the arrival is counted once, on exactly one of the concurrent requests (the first to reach the executor).
// This does not extend to [User]: call [User.Step] and [User.Think] from the scenario goroutine only.
type Step struct {
	name string
	exec *executor
}

// Name returns the step name.
func (s *Step) Name() string { return s.name }

// Request builds a request with an arbitrary method.
func (s *Step) Request(method, path string) *Request {
	return newRequest(s.exec, method, path)
}

// Get builds a GET request.
func (s *Step) Get(path string) *Request { return s.Request(http.MethodGet, path) }

// Post builds a POST request.
func (s *Step) Post(path string) *Request { return s.Request(http.MethodPost, path) }

// Put builds a PUT request.
func (s *Step) Put(path string) *Request { return s.Request(http.MethodPut, path) }

// Patch builds a PATCH request.
func (s *Step) Patch(path string) *Request { return s.Request(http.MethodPatch, path) }

// Delete builds a DELETE request.
func (s *Step) Delete(path string) *Request { return s.Request(http.MethodDelete, path) }

// Parallel runs fns concurrently, each in its own goroutine, waits for all of them and returns their errors
// joined with [errors.Join] (nil when all returned nil), in the order of fns. It never stops early:
// when one function fails, the others still run to completion, so every request they send is recorded.
//
// Typical use is requests that a client sends at the same time:
//
//	return s.Parallel(
//		func() error { return s.Get("/api/profile").Do().ExpectStatus(200) },
//		func() error { return s.Get("/api/cart").Do().ExpectStatus(200) },
//	)
//
// Parallel does not limit the number of requests in flight: all of fns start at once.
// If a function panics, Parallel waits for the others and then panics on the calling goroutine with the first panic
// value (in the order of fns), so the step is recorded as failed with the error kind "panic", the same as when the
// step function itself panics. The stack that is written to Plan.Progress for the first panic of a run is the one
// of that re-raised panic, so it shows Parallel and not the frame of the function that panicked; the panic value is kept.
// A nil function panics like any other.
func (s *Step) Parallel(fns ...func() error) error {
	errs := make([]error, len(fns))
	panics := make([]*parallelPanic, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					panics[i] = &parallelPanic{value: p}
				}
			}()
			errs[i] = fn()
		}()
	}
	wg.Wait()
	for _, p := range panics {
		if p != nil {
			panic(p.value)
		}
	}
	return errors.Join(errs...)
}

// parallelPanic carries a recovered panic value; the pointer tells "no panic" apart from panic(nil).
type parallelPanic struct{ value any }
