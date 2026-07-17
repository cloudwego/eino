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
	"fmt"
	"sort"
	"strings"
)

type ResponseMetaExtension struct {
	ID                   string               `json:"id,omitempty"`
	Status               ResponseStatus       `json:"status,omitempty"`
	Error                *ResponseError       `json:"error,omitempty"`
	IncompleteDetails    *IncompleteDetails   `json:"incomplete_details,omitempty"`
	PreviousResponseID   string               `json:"previous_response_id,omitempty"`
	Reasoning            *Reasoning           `json:"reasoning,omitempty"`
	ServiceTier          ServiceTier          `json:"service_tier,omitempty"`
	CreatedAt            int64                `json:"created_at,omitempty"`
	PromptCacheRetention PromptCacheRetention `json:"prompt_cache_retention,omitempty"`
}

type AssistantGenTextExtension struct {
	Refusal     *OutputRefusal    `json:"refusal,omitempty"`
	Annotations []*TextAnnotation `json:"annotations,omitempty"`
}

type ReasoningExtension struct {
	// Content is the reasoning text content.
	Content []*ReasoningContent `json:"content,omitempty"`
}

type ReasoningContent struct {
	// Index specifies the index position of this content in the final response.
	// Only available in streaming response.
	Index *int `json:"index,omitempty"`

	Text string `json:"text,omitempty"`
}

type ResponseError struct {
	Code    ResponseErrorCode `json:"code,omitempty"`
	Message string            `json:"message,omitempty"`
}

type IncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

type Reasoning struct {
	Effort  ReasoningEffort  `json:"effort,omitempty"`
	Summary ReasoningSummary `json:"summary,omitempty"`
}

type OutputRefusal struct {
	Reason string `json:"reason,omitempty"`
}

type TextAnnotation struct {
	// Index specifies the index position of this annotation in the final response.
	// Only available in streaming response.
	Index int `json:"index,omitempty"`

	Type TextAnnotationType `json:"type,omitempty"`

	FileCitation          *TextAnnotationFileCitation          `json:"file_citation,omitempty"`
	URLCitation           *TextAnnotationURLCitation           `json:"url_citation,omitempty"`
	ContainerFileCitation *TextAnnotationContainerFileCitation `json:"container_file_citation,omitempty"`
	FilePath              *TextAnnotationFilePath              `json:"file_path,omitempty"`
}

type TextAnnotationFileCitation struct {
	// The ID of the file.
	FileID string `json:"file_id,omitempty"`
	// The filename of the file cited.
	Filename string `json:"filename,omitempty"`

	// The index of the file in the list of files.
	Index int `json:"index,omitempty"`
}

type TextAnnotationURLCitation struct {
	// The title of the web resource.
	Title string `json:"title,omitempty"`
	// The URL of the web resource.
	URL string `json:"url,omitempty"`

	// The index of the first character of the URL citation in the message.
	StartIndex int `json:"start_index,omitempty"`
	// The index of the last character of the URL citation in the message.
	EndIndex int `json:"end_index,omitempty"`
}

type TextAnnotationContainerFileCitation struct {
	// The ID of the container file.
	ContainerID string `json:"container_id,omitempty"`

	// The ID of the file.
	FileID string `json:"file_id,omitempty"`
	// The filename of the container file cited.
	Filename string `json:"filename,omitempty"`

	// The index of the first character of the container file citation in the message.
	StartIndex int `json:"start_index,omitempty"`
	// The index of the last character of the container file citation in the message.
	EndIndex int `json:"end_index,omitempty"`
}

type TextAnnotationFilePath struct {
	// The ID of the file.
	FileID string `json:"file_id,omitempty"`

	// The index of the file in the list of files.
	Index int `json:"index,omitempty"`
}

// ConcatAssistantGenTextExtensions concatenates multiple AssistantGenTextExtension chunks into a single one.
func ConcatAssistantGenTextExtensions(chunks []*AssistantGenTextExtension) (*AssistantGenTextExtension, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no assistant generated text extension found")
	}

	ret := &AssistantGenTextExtension{}

	var allAnnotations []*TextAnnotation
	for _, ext := range chunks {
		allAnnotations = append(allAnnotations, ext.Annotations...)
	}

	var (
		indices           []int
		indexToAnnotation = map[int]*TextAnnotation{}
	)

	for _, an := range allAnnotations {
		if an == nil {
			continue
		}
		if indexToAnnotation[an.Index] == nil {
			indexToAnnotation[an.Index] = an
			indices = append(indices, an.Index)
		} else {
			return nil, fmt.Errorf("duplicate annotation index %d", an.Index)
		}
	}

	sort.Slice(indices, func(i, j int) bool {
		return indices[i] < indices[j]
	})

	ret.Annotations = make([]*TextAnnotation, 0, len(indices))
	for _, idx := range indices {
		an := *indexToAnnotation[idx]
		an.Index = 0 // clear index
		ret.Annotations = append(ret.Annotations, &an)
	}

	// Assemble the refusal reason from all chunks with a single pre-sized allocation,
	// building a fresh OutputRefusal so the input chunks are never mutated.
	var (
		refusalParts []string
		refusalLen   int
	)
	for _, ext := range chunks {
		if ext.Refusal == nil {
			continue
		}
		refusalParts = append(refusalParts, ext.Refusal.Reason)
		refusalLen += len(ext.Refusal.Reason)
	}
	if len(refusalParts) > 0 {
		var sb strings.Builder
		sb.Grow(refusalLen)
		for _, p := range refusalParts {
			sb.WriteString(p)
		}
		ret.Refusal = &OutputRefusal{Reason: sb.String()}
	}

	return ret, nil
}

// ConcatReasoningExtensions concatenates multiple ReasoningExtension chunks into a single one.
func ConcatReasoningExtensions(chunks []*ReasoningExtension) (*ReasoningExtension, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no reasoning extension found")
	}

	ret := &ReasoningExtension{}

	var (
		indices      []int
		indexToParts = map[int][]string{}
		indexToLen   = map[int]int{}
		hasIndexed   bool
		hasUnindexed bool
	)

	for _, ext := range chunks {
		if ext == nil {
			continue
		}
		for _, c := range ext.Content {
			if c == nil {
				continue
			}

			if c.Index == nil {
				hasUnindexed = true
				ret.Content = append(ret.Content, &ReasoningContent{Text: c.Text})
				continue
			}

			hasIndexed = true
			idx := *c.Index
			// Accumulate fragments per index; the string is built once below so a
			// long stream sharing one index stays O(total bytes) instead of O(n^2).
			if _, ok := indexToParts[idx]; !ok {
				indices = append(indices, idx)
			}
			indexToParts[idx] = append(indexToParts[idx], c.Text)
			indexToLen[idx] += len(c.Text)
		}
	}

	if hasIndexed && hasUnindexed {
		return nil, fmt.Errorf("reasoning content chunks mix indexed and non-indexed content")
	}

	if hasIndexed {
		sort.Ints(indices)
		ret.Content = make([]*ReasoningContent, 0, len(indices))
		for _, idx := range indices {
			var sb strings.Builder
			sb.Grow(indexToLen[idx])
			for _, p := range indexToParts[idx] {
				sb.WriteString(p)
			}
			ret.Content = append(ret.Content, &ReasoningContent{Text: sb.String()})
		}
	}

	return ret, nil
}

// ConcatResponseMetaExtensions concatenates multiple ResponseMetaExtension chunks into a single one.
func ConcatResponseMetaExtensions(chunks []*ResponseMetaExtension) (*ResponseMetaExtension, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no response meta extension found")
	}
	if len(chunks) == 1 {
		return chunks[0], nil
	}

	ret := &ResponseMetaExtension{}

	for _, ext := range chunks {
		if ext.ID != "" {
			ret.ID = ext.ID
		}
		if ext.Status != "" {
			ret.Status = ext.Status
		}
		if ext.Error != nil {
			ret.Error = ext.Error
		}
		if ext.IncompleteDetails != nil {
			ret.IncompleteDetails = ext.IncompleteDetails
		}
		if ext.PreviousResponseID != "" {
			ret.PreviousResponseID = ext.PreviousResponseID
		}
		if ext.Reasoning != nil {
			ret.Reasoning = ext.Reasoning
		}
		if ext.ServiceTier != "" {
			ret.ServiceTier = ext.ServiceTier
		}
		if ext.CreatedAt != 0 {
			ret.CreatedAt = ext.CreatedAt
		}
		if ext.PromptCacheRetention != "" {
			ret.PromptCacheRetention = ext.PromptCacheRetention
		}
	}

	return ret, nil
}
