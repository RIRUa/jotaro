package jotaro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// ExpectError reports that a response did not match what an Expect* check expected.
// It is classified as record.ErrExpect (expect_mismatch).
type ExpectError struct {
	// Want is the status code that was expected. It is 0 for non-status checks.
	Want int
	// Got is the status code that came back.
	Got int
	// Detail describes a non-status mismatch (header, body or JSON value).
	// It is empty for ExpectStatus.
	Detail string
}

// Error implements the error interface.
func (e *ExpectError) Error() string {
	if e.Detail != "" {
		return "jotaro: " + e.Detail
	}
	return fmt.Sprintf("jotaro: unexpected status code (want %d, got %d)", e.Want, e.Got)
}

// Err returns the network error, if any. It does not look at the HTTP status code.
func (r *Response) Err() error {
	return r.err
}

// Decode reads the body into v as JSON.
// It does so whatever the status code, including 4xx and 5xx, so it can succeed on an error response (see #129);
// check the status first (for example with ExpectStatus) when that matters.
// It returns the network error, if any, without decoding.
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

// ExpectStatusIn checks that the status code is one of codes.
func (r *Response) ExpectStatusIn(codes ...int) error {
	if r.err != nil {
		return r.err
	}
	for _, c := range codes {
		if r.Code == c {
			return nil
		}
	}
	return &ExpectError{Got: r.Code, Detail: fmt.Sprintf("unexpected status code (want one of %v, got %d)", codes, r.Code)}
}

// ExpectHeader checks that the response header key equals want.
func (r *Response) ExpectHeader(key, want string) error {
	if r.err != nil {
		return r.err
	}
	if got := r.Header.Get(key); got != want {
		return &ExpectError{Got: r.Code, Detail: fmt.Sprintf("unexpected header %s (want %q, got %q)", key, want, got)}
	}
	return nil
}

// ExpectBodyContains checks that the body contains substr.
func (r *Response) ExpectBodyContains(substr string) error {
	if r.err != nil {
		return r.err
	}
	if !bytes.Contains(r.Body, []byte(substr)) {
		return &ExpectError{Got: r.Code, Detail: fmt.Sprintf("body does not contain %q", substr)}
	}
	return nil
}

// JSON extracts the value at path from the JSON body.
// path is dot-separated; object keys are looked up by name and array
// elements by index, e.g. "data.items.0.id". An empty path returns the whole body.
// Numbers come back as float64, as with encoding/json.
func (r *Response) JSON(path string) (any, error) {
	if r.err != nil {
		return nil, r.err
	}
	var v any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		return nil, fmt.Errorf("jotaro: failed to decode response (status %d): %w", r.Code, err)
	}
	if path == "" {
		return v, nil
	}
	for _, key := range strings.Split(path, ".") {
		switch cur := v.(type) {
		case map[string]any:
			next, ok := cur[key]
			if !ok {
				return nil, fmt.Errorf("jotaro: JSON path %q: key %q not found", path, key)
			}
			v = next
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(cur) {
				return nil, fmt.Errorf("jotaro: JSON path %q: bad index %q (len %d)", path, key, len(cur))
			}
			v = cur[i]
		default:
			return nil, fmt.Errorf("jotaro: JSON path %q: cannot descend into %T at %q", path, v, key)
		}
	}
	return v, nil
}

// ExpectJSON checks that the value at path in the JSON body equals want.
// Both sides are compared in their JSON form, so ExpectJSON("n", 3) matches {"n": 3}.
func (r *Response) ExpectJSON(path string, want any) error {
	got, err := r.JSON(path)
	if err != nil {
		if r.err != nil {
			return err
		}
		return &ExpectError{Got: r.Code, Detail: err.Error()[len("jotaro: "):]}
	}
	g, _ := json.Marshal(got)
	w, err := json.Marshal(want)
	if err != nil {
		return fmt.Errorf("jotaro: ExpectJSON want is not JSON-encodable: %w", err)
	}
	if !bytes.Equal(g, w) {
		return &ExpectError{Got: r.Code, Detail: fmt.Sprintf("unexpected JSON at %q (want %s, got %s)", path, w, g)}
	}
	return nil
}
