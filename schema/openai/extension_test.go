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
)

func TestConcatResponseMetaExtensions(t *testing.T) {
	t.Run("multiple extensions - takes last non-empty values", func(t *testing.T) {
		err1 := &ResponseError{Code: "err1", Message: "msg1"}
		incomplete := &IncompleteDetails{Reason: "max_tokens"}

		exts := []*ResponseMetaExtension{
			{
				ID:                "id_1",
				Status:            "in_progress",
				Error:             err1,
				IncompleteDetails: nil,
			},
			{
				ID:                "id_2",
				Status:            "",
				Error:             nil,
				IncompleteDetails: nil,
			},
			{
				ID:                "",
				Status:            "completed",
				Error:             nil,
				IncompleteDetails: incomplete,
			},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "id_2", result.ID)
		assert.Equal(t, ResponseStatus("completed"), result.Status)
		assert.Equal(t, err1, result.Error)
		assert.Equal(t, incomplete, result.IncompleteDetails)
	})

	t.Run("streaming scenario", func(t *testing.T) {
		exts := []*ResponseMetaExtension{
			{ID: "chatcmpl_stream", Status: "", Error: nil, IncompleteDetails: nil},
			{ID: "", Status: ResponseStatus("in_progress"), Error: nil, IncompleteDetails: nil},
			{ID: "", Status: ResponseStatus("completed"), Error: nil, IncompleteDetails: nil},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "chatcmpl_stream", result.ID)
		assert.Equal(t, ResponseStatus("completed"), result.Status)
	})
}

func TestConcatAssistantGenTextExtensions(t *testing.T) {
	t.Run("single extension with annotations", func(t *testing.T) {
		ext := &AssistantGenTextExtension{
			Annotations: []*TextAnnotation{
				{
					Index: 0,
					Type:  "file_citation",
					FileCitation: &TextAnnotationFileCitation{
						FileID:   "file_123",
						Filename: "doc.pdf",
					},
				},
			},
		}

		result, err := ConcatAssistantGenTextExtensions([]*AssistantGenTextExtension{ext})
		assert.NoError(t, err)
		assert.Len(t, result.Annotations, 1)
		assert.Equal(t, "file_123", result.Annotations[0].FileCitation.FileID)
	})

	t.Run("multiple extensions - merges annotations by index", func(t *testing.T) {
		exts := []*AssistantGenTextExtension{
			{
				Annotations: []*TextAnnotation{
					{
						Index: 0,
						Type:  "file_citation",
						FileCitation: &TextAnnotationFileCitation{
							FileID: "file_1",
						},
					},
				},
			},
			{
				Annotations: []*TextAnnotation{
					{
						Index: 2,
						Type:  "url_citation",
						URLCitation: &TextAnnotationURLCitation{
							URL: "https://example.com",
						},
					},
				},
			},
			{
				Annotations: []*TextAnnotation{
					{
						Index: 1,
						Type:  "file_path",
						FilePath: &TextAnnotationFilePath{
							FileID: "file_2",
						},
					},
				},
			},
		}

		result, err := ConcatAssistantGenTextExtensions(exts)
		assert.NoError(t, err)
		assert.Len(t, result.Annotations, 3)
		assert.Equal(t, "file_1", result.Annotations[0].FileCitation.FileID)
		assert.Equal(t, "file_2", result.Annotations[1].FilePath.FileID)
		assert.Equal(t, "https://example.com", result.Annotations[2].URLCitation.URL)
	})

	t.Run("streaming scenario - annotations arrive in chunks", func(t *testing.T) {
		exts := []*AssistantGenTextExtension{
			{
				Annotations: []*TextAnnotation{
					{Index: 0, Type: "file_citation", FileCitation: &TextAnnotationFileCitation{FileID: "f1"}},
				},
			},
			{
				Annotations: []*TextAnnotation{
					{Index: 1, Type: "url_citation", URLCitation: &TextAnnotationURLCitation{URL: "url1"}},
				},
			},
			{
				Annotations: []*TextAnnotation{
					{Index: 2, Type: "file_path", FilePath: &TextAnnotationFilePath{FileID: "f2"}},
				},
			},
		}

		result, err := ConcatAssistantGenTextExtensions(exts)
		assert.NoError(t, err)
		assert.Len(t, result.Annotations, 3)
		assert.Equal(t, "f1", result.Annotations[0].FileCitation.FileID)
		assert.Equal(t, "url1", result.Annotations[1].URLCitation.URL)
		assert.Equal(t, "f2", result.Annotations[2].FilePath.FileID)
	})

	t.Run("multiple extensions - concatenates refusal reason", func(t *testing.T) {
		ext1 := &AssistantGenTextExtension{Refusal: &OutputRefusal{Reason: "A"}}
		ext2 := &AssistantGenTextExtension{Refusal: &OutputRefusal{Reason: "B"}}

		result, err := ConcatAssistantGenTextExtensions([]*AssistantGenTextExtension{ext1, ext2})
		assert.NoError(t, err)
		assert.NotNil(t, result.Refusal)
		assert.Equal(t, "AB", result.Refusal.Reason)
	})

	t.Run("duplicate index - error occurrence", func(t *testing.T) {
		exts := []*AssistantGenTextExtension{
			{
				Annotations: []*TextAnnotation{
					{Index: 0, Type: "file_citation", FileCitation: &TextAnnotationFileCitation{FileID: "first"}},
				},
			},
			{
				Annotations: []*TextAnnotation{
					{Index: 0, Type: "url_citation", URLCitation: &TextAnnotationURLCitation{URL: "second"}},
				},
			},
		}

		_, err := ConcatAssistantGenTextExtensions(exts)
		assert.Error(t, err)
	})
}
