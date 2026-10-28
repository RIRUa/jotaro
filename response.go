package jotaro

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Response is the result of a single request.
// HTTP 4xx/5xx are not network errors, so Err stays nil and the status goes into Code.
type Response struct {
	Code   int
	Header http.Header
	Body   []byte

	err error
}

// ExpectError reports that the status code did not match what ExpectStatus expected.
type ExpectError struct {
	Want int
	Got  int
}

// Error implements the error interface.
func (e *ExpectError) Error() string {
	return fmt.Sprintf("jotaro: unexpected status code (want %d, got %d)", e.Want, e.Got)
}

// Err returns the network error, if any. It does not look at the HTTP status code.
func (r *Response) Err() error {
	return r.err
}

// Decode reads the body into v as JSON.
func (r *Response) Decode(v any) error {
	if r.err != nil {
		return r.err
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("jotaro: failed to decode response (status %d): %w", r.Code, err)
	}
	return nil
}

// ExpectStatus checks that the status code equals code.
func (r *Response) ExpectStatus(code int) error {
	if r.err != nil {
		return r.err
	}
	if r.Code != code {
		return &ExpectError{Want: code, Got: r.Code}
	}
	return nil
}
