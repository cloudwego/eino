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

package diag

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/internal/safe"
)

func TestDescribeError_PanicRootCause(t *testing.T) {
	panicErr := safe.NewPanicErr("boom", []byte("goroutine 1"))
	err := fmt.Errorf("outer: %w", panicErr)

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.Equal(t, "*fmt.wrapError", diag.Type)
	assert.Equal(t, "*safe.panicErr", diag.RootCauseType)
	assert.NotContains(t, diag.RootCauseMessage, "goroutine 1")
	assert.Equal(t, "boom", diag.PanicValue)
	assert.Equal(t, "goroutine 1", diag.StackTrace)
	assert.Len(t, diag.Chain, 2)
	assert.Equal(t, "self", diag.Chain[0].Relation)
	assert.Equal(t, "unwrap", diag.Chain[1].Relation)
}

func TestDescribeError_RootCauseError(t *testing.T) {
	lastErr := errors.New("last model error")
	err := &adk.RetryExhaustedError{
		LastErr:      lastErr,
		TotalRetries: 3,
	}

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.Equal(t, "*adk.RetryExhaustedError", diag.Type)
	assert.Equal(t, "*errors.errorString", diag.RootCauseType)
	assert.Equal(t, "last model error", diag.RootCauseMessage)
	require.GreaterOrEqual(t, len(diag.Chain), 2)
	assert.Equal(t, "root_cause", diag.Chain[1].Relation)
	assert.Equal(t, "last model error", diag.Chain[1].Message)
}

func TestDescribeError_PanicMessageOmitsStack(t *testing.T) {
	err := safe.NewPanicErr("panic-value", []byte("stack-value"))

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.NotContains(t, diag.Message, "stack-value")
	assert.NotContains(t, diag.RootCauseMessage, "stack-value")
	assert.Equal(t, "panic-value", diag.PanicValue)
	assert.Equal(t, "stack-value", diag.StackTrace)
	require.NotEmpty(t, diag.Chain)
	assert.NotContains(t, diag.Chain[0].Message, "stack-value")
}

func TestDescribeError_NonComparableError(t *testing.T) {
	err := nonComparableError{
		msg:  "non comparable",
		tags: []string{"tag"},
	}

	assert.NotPanics(t, func() {
		diag := DescribeError(err)
		require.NotNil(t, diag)
		assert.Equal(t, "diag.nonComparableError", diag.Type)
		assert.Equal(t, "non comparable", diag.Message)
		assert.NotEmpty(t, diag.Chain)
	})
}

func TestDescribeError_NonComparablePtrCycle(t *testing.T) {
	a := &nonComparablePtrCycleError{msg: "a", tags: []string{"tag"}}
	b := &nonComparablePtrCycleError{msg: "b", tags: []string{"tag"}}
	a.next = b
	b.next = a

	diag := DescribeError(a)
	require.NotNil(t, diag)
	assert.Equal(t, "*diag.nonComparablePtrCycleError", diag.Type)
	assert.Len(t, diag.Chain, 2, "pointer cycle should be detected by errorSet")
	assert.Equal(t, "self", diag.Chain[0].Relation)
	assert.Equal(t, "unwrap", diag.Chain[1].Relation)
}

type nonComparableError struct {
	msg  string
	tags []string
}

func (e nonComparableError) Error() string {
	return e.msg
}

func (e nonComparableError) RootCause() error {
	return e
}

type nonComparablePtrCycleError struct {
	msg  string
	tags []string
	next error
}

func (e *nonComparablePtrCycleError) Error() string {
	return e.msg
}

func (e *nonComparablePtrCycleError) Unwrap() error {
	return e.next
}

func TestDescribeError_WrappedPanicMessageOmitsStack(t *testing.T) {
	err := fmt.Errorf("outer: %w", safe.NewPanicErr("panic-value", []byte("sensitive stack")))

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.NotContains(t, diag.RootCauseMessage, "sensitive stack")
	assert.Equal(t, "sensitive stack", diag.StackTrace)
	assert.NotContains(t, diag.Chain[1].Message, "sensitive stack")
}

func TestDescribeError_Nil(t *testing.T) {
	assert.Nil(t, DescribeError(nil))
}

func TestDescribeError_NodeError(t *testing.T) {
	err := &nodeError{kind: "NodeRunError", path: []string{"a", "b"}, msg: "node failed"}

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.Equal(t, "NodeRunError", diag.ErrorKind)
	assert.Equal(t, []string{"a", "b"}, diag.NodePath)
	require.NotEmpty(t, diag.Chain)
	assert.Equal(t, "NodeRunError", diag.Chain[0].ErrorKind)
	assert.Equal(t, []string{"a", "b"}, diag.Chain[0].NodePath)
}

func TestDescribeError_SimpleError(t *testing.T) {
	err := errors.New("simple")

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.Equal(t, "*errors.errorString", diag.Type)
	assert.Equal(t, "simple", diag.Message)
	assert.Equal(t, "*errors.errorString", diag.RootCauseType)
	assert.Equal(t, "simple", diag.RootCauseMessage)
	assert.Empty(t, diag.PanicValue)
	assert.Empty(t, diag.StackTrace)
	assert.Empty(t, diag.ErrorKind)
	assert.Empty(t, diag.NodePath)
	assert.Len(t, diag.Chain, 1)
}

func TestDescribeError_LimitsFramesInternally(t *testing.T) {
	errs := make([]error, 0, defaultMaxFrames+1)
	for i := 0; i < defaultMaxFrames+1; i++ {
		errs = append(errs, fmt.Errorf("err %d", i))
	}
	err := multiError{
		msg:  "many errors",
		errs: errs,
	}

	diag := DescribeError(err)
	require.NotNil(t, diag)

	assert.Len(t, diag.Chain, defaultMaxFrames)
	assert.Equal(t, "self", diag.Chain[0].Relation)
	assert.Equal(t, "unwrap[0]", diag.Chain[1].Relation)
	assert.Equal(t, "unwrap[30]", diag.Chain[31].Relation)
}

type multiError struct {
	msg  string
	errs []error
}

func (e multiError) Error() string {
	return e.msg
}

func (e multiError) Unwrap() []error {
	return e.errs
}

type nodeError struct {
	kind string
	path []string
	msg  string
}

func (e *nodeError) Error() string     { return e.msg }
func (e *nodeError) ErrorKind() string { return e.kind }
func (e *nodeError) NodePath() []string {
	out := make([]string, len(e.path))
	copy(out, e.path)
	return out
}

func TestInternalHelpers(t *testing.T) {
	t.Run("rootCause_nil", func(t *testing.T) {
		assert.Nil(t, rootCause(nil, 10))
	})

	t.Run("messageFor_nil", func(t *testing.T) {
		assert.Equal(t, "", messageFor(nil))
	})

	t.Run("errorType_nil", func(t *testing.T) {
		assert.Equal(t, "", errorType(nil))
	})

	t.Run("cloneStrings_nil", func(t *testing.T) {
		assert.Nil(t, cloneStrings(nil))
		assert.Nil(t, cloneStrings([]string{}))
	})

	t.Run("sameError_different_values", func(t *testing.T) {
		a := errors.New("a")
		b := fmt.Errorf("b: %w", a)
		assert.False(t, sameError(a, b))
	})

	t.Run("sameError_unhashable_values", func(t *testing.T) {
		a := nonComparableError{msg: "a", tags: []string{"1"}}
		b := nonComparableError{msg: "a", tags: []string{"1"}}
		assert.False(t, sameError(a, b))
	})

	t.Run("errorSet_unhashable_value_type", func(t *testing.T) {
		s := newErrorSet()
		e := nonComparableError{msg: "x", tags: []string{"t"}}
		s.add(e)
		assert.False(t, s.contains(e))
	})

}
