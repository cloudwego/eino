/*
 * Copyright 2025 CloudWeGo Authors
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

package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcatAssistantGenTextExtensions_Refusal(t *testing.T) {
	t.Run("refusal reason concatenated in order", func(t *testing.T) {
		chunks := []*AssistantGenTextExtension{
			{Refusal: &OutputRefusal{Reason: "I "}},
			{},
			{Refusal: &OutputRefusal{Reason: "can't "}},
			{Refusal: &OutputRefusal{Reason: "help"}},
		}
		got, err := ConcatAssistantGenTextExtensions(chunks)
		require.NoError(t, err)
		require.NotNil(t, got.Refusal)
		assert.Equal(t, "I can't help", got.Refusal.Reason)
	})

	t.Run("does not mutate input refusal chunks", func(t *testing.T) {
		chunks := []*AssistantGenTextExtension{
			{Refusal: &OutputRefusal{Reason: "a"}},
			{Refusal: &OutputRefusal{Reason: "b"}},
		}
		_, err := ConcatAssistantGenTextExtensions(chunks)
		require.NoError(t, err)
		assert.Equal(t, "a", chunks[0].Refusal.Reason)
		assert.Equal(t, "b", chunks[1].Refusal.Reason)
	})

	t.Run("no refusal yields nil", func(t *testing.T) {
		chunks := []*AssistantGenTextExtension{{}, {}}
		got, err := ConcatAssistantGenTextExtensions(chunks)
		require.NoError(t, err)
		assert.Nil(t, got.Refusal)
	})
}

func TestConcatReasoningExtensions_IndexedText(t *testing.T) {
	idx := func(i int) *int { return &i }

	t.Run("same index merged in stream order", func(t *testing.T) {
		chunks := []*ReasoningExtension{
			{Content: []*ReasoningContent{{Index: idx(0), Text: "he"}}},
			{Content: []*ReasoningContent{{Index: idx(1), Text: "AA"}}},
			{Content: []*ReasoningContent{{Index: idx(0), Text: "llo"}}},
			{Content: []*ReasoningContent{{Index: idx(1), Text: "BB"}}},
		}
		got, err := ConcatReasoningExtensions(chunks)
		require.NoError(t, err)
		require.Len(t, got.Content, 2)
		assert.Equal(t, "hello", got.Content[0].Text)
		assert.Equal(t, "AABB", got.Content[1].Text)
	})

	t.Run("indices sorted ascending", func(t *testing.T) {
		chunks := []*ReasoningExtension{
			{Content: []*ReasoningContent{{Index: idx(2), Text: "c"}}},
			{Content: []*ReasoningContent{{Index: idx(0), Text: "a"}}},
			{Content: []*ReasoningContent{{Index: idx(1), Text: "b"}}},
		}
		got, err := ConcatReasoningExtensions(chunks)
		require.NoError(t, err)
		require.Len(t, got.Content, 3)
		assert.Equal(t, "a", got.Content[0].Text)
		assert.Equal(t, "b", got.Content[1].Text)
		assert.Equal(t, "c", got.Content[2].Text)
	})

	t.Run("unindexed content kept in order", func(t *testing.T) {
		chunks := []*ReasoningExtension{
			{Content: []*ReasoningContent{{Text: "x"}}},
			{Content: []*ReasoningContent{{Text: "y"}}},
		}
		got, err := ConcatReasoningExtensions(chunks)
		require.NoError(t, err)
		require.Len(t, got.Content, 2)
		assert.Equal(t, "x", got.Content[0].Text)
		assert.Equal(t, "y", got.Content[1].Text)
	})

	t.Run("mixing indexed and unindexed errors", func(t *testing.T) {
		chunks := []*ReasoningExtension{
			{Content: []*ReasoningContent{{Index: idx(0), Text: "a"}}},
			{Content: []*ReasoningContent{{Text: "b"}}},
		}
		_, err := ConcatReasoningExtensions(chunks)
		require.Error(t, err)
	})

	t.Run("does not mutate input content", func(t *testing.T) {
		chunks := []*ReasoningExtension{
			{Content: []*ReasoningContent{{Index: idx(0), Text: "a"}}},
			{Content: []*ReasoningContent{{Index: idx(0), Text: "b"}}},
		}
		_, err := ConcatReasoningExtensions(chunks)
		require.NoError(t, err)
		assert.Equal(t, "a", chunks[0].Content[0].Text)
		assert.Equal(t, "b", chunks[1].Content[0].Text)
	})
}
