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

package team

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarshalToolResult_NormalMap(t *testing.T) {
	data := map[string]any{
		"status": "ok",
		"count":  42,
	}
	result := marshalToolResult(data)
	assert.Contains(t, result, `"status"`)
	assert.Contains(t, result, `"ok"`)
	assert.Contains(t, result, `"count"`)
	assert.Contains(t, result, `42`)
}

func TestMarshalToolResult_EmptyMap(t *testing.T) {
	data := map[string]any{}
	result := marshalToolResult(data)
	assert.Equal(t, "{}", result)
}

func TestMarshalToolResult_NilMap(t *testing.T) {
	result := marshalToolResult(nil)
	assert.NotEmpty(t, result)
}

func TestMarshalToolResult_MarshalError(t *testing.T) {
	ch := make(chan int)
	data := map[string]any{
		"bad": ch,
	}
	result := marshalToolResult(data)
	assert.Contains(t, result, "error")
}

func TestJoinErrors_AllNil(t *testing.T) {
	err := joinErrors(nil, nil, nil)
	assert.Nil(t, err)
}

func TestJoinErrors_NoArgs(t *testing.T) {
	err := joinErrors()
	assert.Nil(t, err)
}

func TestJoinErrors_SingleError(t *testing.T) {
	original := errors.New("something failed")
	err := joinErrors(original)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "something failed")
}

func TestJoinErrors_MultipleErrors(t *testing.T) {
	e1 := errors.New("error one")
	e2 := errors.New("error two")
	e3 := errors.New("error three")
	err := joinErrors(e1, e2, e3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error one")
	assert.Contains(t, err.Error(), "error two")
	assert.Contains(t, err.Error(), "error three")
	assert.Contains(t, err.Error(), "; ")
}

func TestJoinErrors_MixedNilAndNonNil(t *testing.T) {
	e1 := errors.New("real error")
	err := joinErrors(nil, e1, nil)
	assert.Error(t, err)
	assert.Equal(t, "real error", err.Error())
}

func TestMultiError_Error(t *testing.T) {
	me := &multiError{
		errs: []error{
			errors.New("a"),
			errors.New("b"),
		},
	}
	assert.Equal(t, "a; b", me.Error())
}

func TestMultiError_Unwrap(t *testing.T) {
	e1 := errors.New("first")
	e2 := errors.New("second")
	me := &multiError{errs: []error{e1, e2}}

	unwrapped := me.Unwrap()
	assert.Len(t, unwrapped, 2)
	assert.Equal(t, e1, unwrapped[0])
	assert.Equal(t, e2, unwrapped[1])
}

func TestMultiError_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	other := errors.New("other")
	combined := joinErrors(sentinel, other)
	assert.True(t, errors.Is(combined, sentinel))
	assert.True(t, errors.Is(combined, other))
}

func TestSafeGoWithLogger_NormalExecution(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	executed := false
	safeGoWithLogger(nopLogger{}, func() {
		defer wg.Done()
		executed = true
	})
	wg.Wait()
	assert.True(t, executed)
}

func TestSafeGoWithLogger_PanicRecovery(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	logged := false
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged = true
		wg.Done()
	}}

	safeGoWithLogger(logger, func() {
		panic("test panic")
	})
	wg.Wait()
	assert.True(t, logged)
}

type testLogger struct {
	onPrintf func(format string, args ...any)
}

func (l *testLogger) Printf(format string, args ...any) {
	l.onPrintf(format, args...)
}

func TestSelectToolDesc_ReturnsNonEmpty(t *testing.T) {
	result := selectToolDesc("english desc", "chinese desc")
	assert.NotEmpty(t, result)
	assert.True(t, result == "english desc" || result == "chinese desc")
}

func TestSelectToolDesc_EmptyInputs(t *testing.T) {
	result := selectToolDesc("", "")
	assert.Equal(t, "", result)
}
