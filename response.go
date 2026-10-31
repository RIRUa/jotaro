package jotaro

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Response is the result of a single request.
// HTTP 4xx/5xx are not network errors, so Err stays nil and the status goes into Code.
type Response struct {
	// Code is the HTTP status code. It is 0 when no response was received.
	Code int
	// Header holds the response headers. It is nil when no response was received.
	Header http.Header
	// Body is the whole body, already read.
	Body []byte

	// err is the network or build error, if any.
	err error
}

// ExpectError reports that the status code did not match what ExpectStatus expected.
type ExpectError struct {
	// Want is the status code that was expected.
	Want int
	// Got is the status code that came back.
	Got int
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
