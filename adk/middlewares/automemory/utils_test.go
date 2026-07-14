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

package automemory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestConcatMessageStream_MultipleChunks(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "hello "},
		{Role: schema.Assistant, Content: "world"},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, schema.Assistant, msg.Role)
	assert.Equal(t, "hello world", msg.Content)
}

func TestConcatMessageStream_SingleChunk(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "only one"},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "only one", msg.Content)
}

func TestConcatMessageStream_EmptyStream(t *testing.T) {
	r := schema.StreamReaderFromArray([]*schema.Message{})

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestConcatMessageStream_ConcatError(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "a"},
		{Role: schema.User, Content: "b"},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.Error(t, err)
	assert.Nil(t, msg)
	assert.Contains(t, err.Error(), "different roles")
}

func TestConcatMessageStream_WithToolCalls(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "thinking..."},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"test"}`}},
		}},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "thinking...", msg.Content)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, "search", msg.ToolCalls[0].Function.Name)
}
