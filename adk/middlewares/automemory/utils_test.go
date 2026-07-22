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

func TestParseTopicSelectionFromContent_PureJSON(t *testing.T) {
	valid := map[string]struct{}{
		"debugging.md": {},
		"patterns.md":  {},
		"notes.md":     {},
	}
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: `{"selected_memories": ["debugging.md", "patterns.md"]}`,
	}

	selected, err := parseTopicSelectionFromContent[*schema.Message](msg, valid)
	require.NoError(t, err)
	assert.Equal(t, []string{"debugging.md", "patterns.md"}, selected)
}

func TestParseTopicSelectionFromContent_MarkdownCodeBlock(t *testing.T) {
	valid := map[string]struct{}{
		"debugging.md": {},
		"patterns.md":  {},
	}
	msg := &schema.Message{
		Role: schema.Assistant,
		Content: "Here is my selection:\n```json\n{\"selected_memories\": [\"debugging.md\"]}\n```\n",
	}

	selected, err := parseTopicSelectionFromContent[*schema.Message](msg, valid)
	require.NoError(t, err)
	assert.Equal(t, []string{"debugging.md"}, selected)
}

func TestParseTopicSelectionFromContent_FiltersInvalidPaths(t *testing.T) {
	valid := map[string]struct{}{
		"debugging.md": {},
	}
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: `{"selected_memories": ["debugging.md", "nonexistent.md"]}`,
	}

	selected, err := parseTopicSelectionFromContent[*schema.Message](msg, valid)
	require.NoError(t, err)
	assert.Equal(t, []string{"debugging.md"}, selected)
}

func TestParseTopicSelectionFromContent_EmptyContent(t *testing.T) {
	valid := map[string]struct{}{"a.md": {}}
	msg := &schema.Message{Role: schema.Assistant, Content: ""}

	_, err := parseTopicSelectionFromContent[*schema.Message](msg, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response content")
}

func TestParseTopicSelectionFromContent_NoJSON(t *testing.T) {
	valid := map[string]struct{}{"a.md": {}}
	msg := &schema.Message{Role: schema.Assistant, Content: "I don't know what to pick."}

	_, err := parseTopicSelectionFromContent[*schema.Message](msg, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no JSON found")
}

func TestParseTopicSelectionFromContent_EmptySelection(t *testing.T) {
	valid := map[string]struct{}{"a.md": {}}
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: `{"selected_memories": []}`,
	}

	selected, err := parseTopicSelectionFromContent[*schema.Message](msg, valid)
	require.NoError(t, err)
	assert.Empty(t, selected)
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "pure json",
			input: `{"selected_memories": ["a.md"]}`,
			want:  `{"selected_memories": ["a.md"]}`,
		},
		{
			name:  "json with prefix text",
			input: "Here are the results:\n{\"selected_memories\": [\"b.md\"]}",
			want:  `{"selected_memories": ["b.md"]}`,
		},
		{
			name:  "markdown json block",
			input: "```json\n{\"selected_memories\": [\"c.md\"]}\n```",
			want:  `{"selected_memories": ["c.md"]}`,
		},
		{
			name:  "markdown generic block with json",
			input: "```\n{\"selected_memories\": [\"d.md\"]}\n```",
			want:  `{"selected_memories": ["d.md"]}`,
		},
		{
			name:  "no json",
			input: "I cannot select any memories.",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTopicSelectionJSONSchema(t *testing.T) {
	s := topicSelectionJSONSchema()
	require.NotNil(t, s)
	assert.Equal(t, "object", s.Type)
	props := s.Properties
	require.NotNil(t, props)
	val, ok := props.Get("selected_memories")
	require.True(t, ok)
	assert.Equal(t, "array", val.Type)
}
