/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package diag provides structured error diagnostics for the eino framework.
package diag

import (
	"fmt"
	"reflect"
)

const defaultMaxFrames = 128

// PanicError is implemented by errors that carry a recovered panic value and
// stack trace.
type PanicError interface {
	error
	PanicValue() any
	StackTrace() []byte
}

// NodeError is implemented by errors that can identify the graph node path
// where the failure happened.
type NodeError interface {
	error
	ErrorKind() string
	NodePath() []string
}

// RootCauseError is implemented by wrapper errors whose diagnostic root cause
// differs from their Go error unwrap target.
type RootCauseError interface {
	error
	RootCause() error
}

// ErrorDiagnostic is a stable, machine-readable view of an error chain.
//
// Message uses err.Error() directly, except for PanicError where the stack
// trace is replaced with [omitted]. The full stack is available in StackTrace.
type ErrorDiagnostic struct {
	Type             string       `json:"type,omitempty"`
	Message          string       `json:"message,omitempty"`
	RootCauseType    string       `json:"root_cause_type,omitempty"`
	RootCauseMessage string       `json:"root_cause_message,omitempty"`
	ErrorKind        string       `json:"error_kind,omitempty"`
	NodePath         []string     `json:"node_path,omitempty"`
	PanicValue       string       `json:"panic_value,omitempty"`
	StackTrace       string       `json:"stack_trace,omitempty"`
	Chain            []ErrorFrame `json:"chain,omitempty"`
}

// ErrorFrame describes one error in the diagnostic chain.
type ErrorFrame struct {
	Type       string   `json:"type,omitempty"`
	Message    string   `json:"message,omitempty"`
	Relation   string   `json:"relation,omitempty"`
	ErrorKind  string   `json:"error_kind,omitempty"`
	NodePath   []string `json:"node_path,omitempty"`
	PanicValue string   `json:"panic_value,omitempty"`
	StackTrace string   `json:"stack_trace,omitempty"`
}

// DescribeError converts err into a stable, machine-readable diagnostic.
//
// It preserves the standard Go error chain while also following RootCause()
// links, which lets errors expose richer diagnostic causes without changing
// their Unwrap behavior.
//
// Chain includes frames from every branch: if an error implements both
// RootCause() and Unwrap(), both paths are expanded. RootCauseType and
// RootCauseMessage reflect the deepest single cause found by following
// RootCause → Unwrap in priority order. For multi-error (Unwrap() []error),
// only the first child is followed — there is no meaningful "single root
// cause" for a group of independent errors; inspect Chain for the full tree.
func DescribeError(err error) *ErrorDiagnostic {
	if err == nil {
		return nil
	}

	root := rootCause(err, defaultMaxFrames)
	remaining := defaultMaxFrames
	diag := &ErrorDiagnostic{
		Type:             errorType(err),
		Message:          messageFor(err),
		RootCauseType:    errorType(root),
		RootCauseMessage: messageFor(root),
		Chain:            collectFrames(err, nil, "self", &remaining),
	}

	for _, frame := range diag.Chain {
		if diag.ErrorKind == "" && frame.ErrorKind != "" {
			diag.ErrorKind = frame.ErrorKind
		}
		if len(diag.NodePath) == 0 && len(frame.NodePath) > 0 {
			diag.NodePath = cloneStrings(frame.NodePath)
		}
		if diag.PanicValue == "" && frame.PanicValue != "" {
			diag.PanicValue = frame.PanicValue
		}
		if diag.StackTrace == "" && frame.StackTrace != "" {
			diag.StackTrace = frame.StackTrace
		}
	}

	return diag
}

func collectFrames(err error, seen *errorSet, relation string, remaining *int) []ErrorFrame {
	if err == nil || remaining == nil || *remaining <= 0 {
		return nil
	}
	if seen == nil {
		seen = newErrorSet()
	}
	if seen.contains(err) {
		return nil
	}
	seen.add(err)

	*remaining = *remaining - 1
	frames := []ErrorFrame{newFrame(err, relation)}

	if e, ok := err.(interface{ RootCause() error }); ok {
		cause := e.RootCause()
		if cause != nil && !sameError(cause, err) {
			frames = append(frames, collectFrames(cause, seen, "root_cause", remaining)...)
		}
	}

	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for i, child := range e.Unwrap() {
			frames = append(frames, collectFrames(child, seen, fmt.Sprintf("unwrap[%d]", i), remaining)...)
		}
	case interface{ Unwrap() error }:
		frames = append(frames, collectFrames(e.Unwrap(), seen, "unwrap", remaining)...)
	}

	return frames
}

func newFrame(err error, relation string) ErrorFrame {
	frame := ErrorFrame{
		Type:     errorType(err),
		Message:  messageFor(err),
		Relation: relation,
	}
	if e, ok := err.(interface {
		ErrorKind() string
		NodePath() []string
	}); ok {
		frame.ErrorKind = e.ErrorKind()
		frame.NodePath = cloneStrings(e.NodePath())
	}
	if e, ok := err.(interface {
		PanicValue() any
		StackTrace() []byte
	}); ok {
		frame.PanicValue = fmt.Sprint(e.PanicValue())
		frame.StackTrace = string(e.StackTrace())
	}
	return frame
}

func rootCause(err error, maxDepth int) error {
	if err == nil {
		return nil
	}
	cur := err
	seen := newErrorSet()
	for i := 0; i < maxDepth && cur != nil; i++ {
		if seen.contains(cur) {
			break
		}
		seen.add(cur)

		if e, ok := cur.(interface{ RootCause() error }); ok {
			if cause := e.RootCause(); cause != nil && !sameError(cause, cur) {
				cur = cause
				continue
			}
		}
		if e, ok := cur.(interface{ Unwrap() error }); ok {
			if cause := e.Unwrap(); cause != nil && !sameError(cause, cur) {
				cur = cause
				continue
			}
		}
		if e, ok := cur.(interface{ Unwrap() []error }); ok {
			causes := e.Unwrap()
			if len(causes) > 0 && causes[0] != nil && !sameError(causes[0], cur) {
				cur = causes[0]
				continue
			}
		}
		break
	}
	return cur
}

func messageFor(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := err.(interface {
		PanicValue() any
		StackTrace() []byte
	}); ok {
		return fmt.Sprintf("panic error: %v, \nstack: [omitted]", e.PanicValue())
	}
	return err.Error()
}

type errorSet struct {
	hashable map[error]struct{}
}

func newErrorSet() *errorSet {
	return &errorSet{
		hashable: make(map[error]struct{}),
	}
}

func (s *errorSet) contains(err error) bool {
	found, hashable := s.hashLookup(err)
	if hashable {
		return found
	}
	return false
}

func (s *errorSet) add(err error) {
	_, hashable := s.hashLookup(err)
	if hashable {
		s.hashable[err] = struct{}{}
	}
}

func (s *errorSet) hashLookup(err error) (found, hashable bool) {
	defer func() {
		if recover() != nil {
			found, hashable = false, false
		}
	}()
	_, found = s.hashable[err]
	return found, true
}

func sameError(a, b error) (same bool) {
	defer func() {
		_ = recover()
	}()
	return a == b
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return reflect.TypeOf(err).String()
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
