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

package compose

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFieldMappingErrorCompileContext(t *testing.T) {
	t.Run("source field not found", func(t *testing.T) {
		wf := NewWorkflow[struct{ Value string }, string]()
		wf.End().AddInput(START, FromField("Missing"))

		_, err := wf.Compile(context.Background())
		require.ErrorContains(t, err, "has no field[Missing]")

		var mappingErr *FieldMappingError
		require.ErrorAs(t, err, &mappingErr)
		require.Equal(t, START, mappingErr.FromNode)
		require.Equal(t, END, mappingErr.ToNode)
		require.Equal(t, "Missing", mappingErr.FromField)
		require.Empty(t, mappingErr.ToField)
		require.Equal(t, FieldMappingErrorReasonFieldNotFound, mappingErr.Reason)
		require.Equal(t, reflect.TypeOf(struct{ Value string }{}), mappingErr.ActualType)
	})

	t.Run("target field not exported", func(t *testing.T) {
		wf := NewWorkflow[string, struct{ hidden string }]()
		wf.End().AddInput(START, ToField("hidden"))

		_, err := wf.Compile(context.Background())
		require.ErrorContains(t, err, "has an unexported field[hidden]")

		var mappingErr *FieldMappingError
		require.ErrorAs(t, err, &mappingErr)
		require.Equal(t, START, mappingErr.FromNode)
		require.Equal(t, END, mappingErr.ToNode)
		require.Empty(t, mappingErr.FromField)
		require.Equal(t, "hidden", mappingErr.ToField)
		require.Equal(t, FieldMappingErrorReasonFieldNotExported, mappingErr.Reason)
	})
}

func TestFieldMappingErrorRuntimeContext(t *testing.T) {
	t.Run("missing nested source field", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, string]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"item", "Missing"}))
		runnable, err := wf.Compile(context.Background())
		require.NoError(t, err)

		input := map[string]fmt.Stringer{"item": fieldMappingStringer{Value: "value"}}
		_, err = runnable.Invoke(context.Background(), input)
		assertRuntimeFieldMappingError(t, err, FieldMappingErrorReasonFieldNotFound, "item.Missing", nil, reflect.TypeOf(fieldMappingStringer{}))

		output, err := runnable.Stream(context.Background(), input)
		require.NoError(t, err)
		_, err = output.Recv()
		assertRuntimeFieldMappingError(t, err, FieldMappingErrorReasonFieldNotFound, "item.Missing", nil, reflect.TypeOf(fieldMappingStringer{}))
		output.Close()
	})

	t.Run("deferred type mismatch", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, int]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"item", "Value"}))
		runnable, err := wf.Compile(context.Background())
		require.NoError(t, err)

		input := map[string]fmt.Stringer{"item": fieldMappingStringer{Value: "value"}}
		_, err = runnable.Invoke(context.Background(), input)
		assertRuntimeFieldMappingError(t, err, FieldMappingErrorReasonTypeMismatch, "item.Value",
			reflect.TypeOf(int(0)), reflect.TypeOf(""))
	})
}

func assertRuntimeFieldMappingError(t *testing.T, err error, reason FieldMappingErrorReason, fromField string,
	expectedType, actualType reflect.Type) {
	t.Helper()
	require.Error(t, err)

	var mappingErr *FieldMappingError
	require.True(t, errors.As(err, &mappingErr))
	require.Equal(t, START, mappingErr.FromNode)
	require.Equal(t, END, mappingErr.ToNode)
	require.Equal(t, fromField, mappingErr.FromField)
	require.Empty(t, mappingErr.ToField)
	require.Equal(t, reason, mappingErr.Reason)
	require.Equal(t, expectedType, mappingErr.ExpectedType)
	require.Equal(t, actualType, mappingErr.ActualType)
}

type fieldMappingStringer struct {
	Value string
}

func (f fieldMappingStringer) String() string {
	return f.Value
}
