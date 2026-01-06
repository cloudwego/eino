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

package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConcatAssistantGenTextExtensions(t *testing.T) {
	t.Run("multiple extensions - concatenates all citations", func(t *testing.T) {
		exts := []*AssistantGenTextExtension{
			{
				Citations: []*TextCitation{
					{
						Type: "char_location",
						CharLocation: &CitationCharLocation{
							CitedText:     "citation 1",
							DocumentIndex: 0,
						},
					},
				},
			},
			{
				Citations: []*TextCitation{
					{
						Type: "page_location",
						PageLocation: &CitationPageLocation{
							CitedText:       "citation 2",
							StartPageNumber: 1,
							EndPageNumber:   2,
						},
					},
					{
						Type: "web_search_result_location",
						WebSearchResultLocation: &CitationWebSearchResultLocation{
							CitedText: "citation 3",
							URL:       "https://example.com",
						},
					},
				},
			},
			{
				Citations: []*TextCitation{
					{
						Type: "content_block_location",
						ContentBlockLocation: &CitationContentBlockLocation{
							CitedText:       "citation 4",
							StartBlockIndex: 0,
							EndBlockIndex:   5,
						},
					},
				},
			},
		}

		result, err := ConcatAssistantGenTextExtensions(exts)
		assert.NoError(t, err)
		assert.Len(t, result.Citations, 4)
		assert.Equal(t, "citation 1", result.Citations[0].CharLocation.CitedText)
		assert.Equal(t, "citation 2", result.Citations[1].PageLocation.CitedText)
		assert.Equal(t, "citation 3", result.Citations[2].WebSearchResultLocation.CitedText)
		assert.Equal(t, "citation 4", result.Citations[3].ContentBlockLocation.CitedText)
	})

	t.Run("mixed empty and non-empty citations", func(t *testing.T) {
		exts := []*AssistantGenTextExtension{
			{Citations: nil},
			{
				Citations: []*TextCitation{
					{
						Type: "char_location",
						CharLocation: &CitationCharLocation{
							CitedText: "text1",
						},
					},
				},
			},
			{Citations: []*TextCitation{}},
			{
				Citations: []*TextCitation{
					{
						Type: "page_location",
						PageLocation: &CitationPageLocation{
							CitedText: "text2",
						},
					},
				},
			},
		}

		result, err := ConcatAssistantGenTextExtensions(exts)
		assert.NoError(t, err)
		assert.Len(t, result.Citations, 2)
		assert.Equal(t, "text1", result.Citations[0].CharLocation.CitedText)
		assert.Equal(t, "text2", result.Citations[1].PageLocation.CitedText)
	})

	t.Run("streaming scenario - citations arrive in chunks", func(t *testing.T) {
		// Simulates streaming where citations arrive progressively
		exts := []*AssistantGenTextExtension{
			{
				Citations: []*TextCitation{
					{Type: "char_location", CharLocation: &CitationCharLocation{CitedText: "chunk1"}},
				},
			},
			{
				Citations: []*TextCitation{
					{Type: "char_location", CharLocation: &CitationCharLocation{CitedText: "chunk2"}},
				},
			},
			{
				Citations: []*TextCitation{
					{Type: "char_location", CharLocation: &CitationCharLocation{CitedText: "chunk3"}},
				},
			},
		}

		result, err := ConcatAssistantGenTextExtensions(exts)
		assert.NoError(t, err)
		assert.Len(t, result.Citations, 3)
		assert.Equal(t, "chunk1", result.Citations[0].CharLocation.CitedText)
		assert.Equal(t, "chunk2", result.Citations[1].CharLocation.CitedText)
		assert.Equal(t, "chunk3", result.Citations[2].CharLocation.CitedText)
	})
}

func TestConcatResponseMetaExtensions(t *testing.T) {
	t.Run("multiple extensions - takes last non-empty values", func(t *testing.T) {
		exts := []*ResponseMetaExtension{
			{
				ID:         "msg_1",
				StopReason: "stop_1",
			},
			{
				ID:         "msg_2",
				StopReason: "",
			},
			{
				ID:         "",
				StopReason: "stop_3",
			},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "msg_2", result.ID)          // Last non-empty ID
		assert.Equal(t, "stop_3", result.StopReason) // Last non-empty StopReason
	})

	t.Run("all empty fields", func(t *testing.T) {
		exts := []*ResponseMetaExtension{
			{ID: "", StopReason: ""},
			{ID: "", StopReason: ""},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "", result.ID)
		assert.Equal(t, "", result.StopReason)
	})

	t.Run("streaming scenario - ID in first chunk, StopReason in last", func(t *testing.T) {
		exts := []*ResponseMetaExtension{
			{ID: "msg_stream_123", StopReason: ""},
			{ID: "", StopReason: ""},
			{ID: "", StopReason: "end_turn"},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "msg_stream_123", result.ID)
		assert.Equal(t, "end_turn", result.StopReason)
	})
}
