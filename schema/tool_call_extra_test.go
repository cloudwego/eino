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

package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcatMessagesPreservesToolCallExtra(t *testing.T) {
	first, second := 0, 1
	firstExtra := map[string]any{"provider": "test"}
	lastExtra := map[string]any{"signature": "opaque-signature"}
	chunks := []*Message{
		{Role: Assistant, ToolCalls: []ToolCall{
			{Index: &first, ID: "call_1", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{"q":`}, Extra: firstExtra},
			{Index: &second, ID: "call_2", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{"q":`}},
		}},
		{ToolCalls: []ToolCall{
			{Index: &first, Function: FunctionCall{Arguments: `"one"}`}, Extra: lastExtra},
			{Index: &second, Function: FunctionCall{Arguments: `"two"}`}, Extra: map[string]any{"signature": "other-signature"}},
		}},
	}

	msg, err := ConcatMessages(chunks)
	require.NoError(t, err)
	require.Len(t, msg.ToolCalls, 2)
	assert.Equal(t, `{"q":"one"}`, msg.ToolCalls[0].Function.Arguments)
	assert.Equal(t, map[string]any{"provider": "test", "signature": "opaque-signature"}, msg.ToolCalls[0].Extra)
	assert.Equal(t, map[string]any{"signature": "other-signature"}, msg.ToolCalls[1].Extra)

	msg.ToolCalls[0].Extra["provider"] = "changed"
	assert.Equal(t, map[string]any{"provider": "test"}, firstExtra)
	assert.Equal(t, map[string]any{"signature": "opaque-signature"}, lastExtra)
}

func TestConcatMessagesToolCallExtraUsesConcatRules(t *testing.T) {
	index := 0
	t.Run("string fragments", func(t *testing.T) {
		msg, err := ConcatMessages([]*Message{
			{ToolCalls: []ToolCall{{Index: &index, Extra: map[string]any{"fragment": "first"}}}},
			{ToolCalls: []ToolCall{{Index: &index, Extra: map[string]any{"fragment": "second"}}}},
		})
		require.NoError(t, err)
		assert.Equal(t, "firstsecond", msg.ToolCalls[0].Extra["fragment"])
	})
	t.Run("incompatible types", func(t *testing.T) {
		_, err := ConcatMessages([]*Message{
			{ToolCalls: []ToolCall{{Index: &index, Extra: map[string]any{"value": "text"}}}},
			{ToolCalls: []ToolCall{{Index: &index, Extra: map[string]any{"value": 1}}}},
		})
		require.Error(t, err)
	})
}
