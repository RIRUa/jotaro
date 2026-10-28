package jotaro

import "net/http"

// Step is the entry point for building requests inside u.Step.
// Every request built from it is recorded under this step's name.
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
