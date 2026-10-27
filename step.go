package jotaro

import "net/http"

// Step は u.Step の中でリクエストを組み立てるための入口。
// ここから作ったリクエストはすべて、このステップ名で記録される。
type Step struct {
	name string
	exec *executor
}

// Name はステップ名を返す。
func (s *Step) Name() string { return s.name }

// Request は任意のメソッドでリクエストを組み立てる。
func (s *Step) Request(method, path string) *Request {
	return newRequest(s.exec, method, path)
}

func (s *Step) Get(path string) *Request    { return s.Request(http.MethodGet, path) }
func (s *Step) Post(path string) *Request   { return s.Request(http.MethodPost, path) }
func (s *Step) Put(path string) *Request    { return s.Request(http.MethodPut, path) }
func (s *Step) Patch(path string) *Request  { return s.Request(http.MethodPatch, path) }
func (s *Step) Delete(path string) *Request { return s.Request(http.MethodDelete, path) }
